package studio

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/snowmerak/q/diagnostics"
)

// ServerOption configures a Studio server without changing process-wide state.
type ServerOption func(*serverOptions)

type serverOptions struct {
	logger *slog.Logger
}

// WithLogger enables Studio diagnostics. Servers are silent by default.
func WithLogger(logger *slog.Logger) ServerOption {
	return func(options *serverOptions) {
		if logger != nil {
			options.logger = logger
		}
	}
}

func studioLogger(options []ServerOption) *slog.Logger {
	settings := serverOptions{logger: slog.New(slog.DiscardHandler)}
	for _, option := range options {
		option(&settings)
	}
	return settings.logger
}

func logRequests(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		ctx := diagnostics.WithRequestID(request.Context(), request.Header.Get("X-Request-ID"))
		ctx = diagnostics.WithLogger(ctx, logger)
		request = request.WithContext(ctx)
		writer.Header().Set("X-Request-ID", diagnostics.RequestID(ctx))
		defer func() {
			if value := recover(); value != nil {
				diagnostics.Logger(ctx).Error("studio.request.panicked", "reason_code", "handler_panic")
				panic(value)
			}
		}()
		response := &loggedResponseWriter{ResponseWriter: writer}
		next.ServeHTTP(response, request)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelDebug
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		// Query strings and bodies can contain API keys, prompts, and file data.
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		attributes := []any{
			"method", request.Method, "route", route,
			"status", status, "duration_ms", time.Since(started).Milliseconds(),
		}
		if response.apiError != nil {
			attributes = append(attributes, "error", response.apiError)
		}
		diagnostics.Logger(ctx).Log(ctx, level, "studio.request.completed", attributes...)
	})
}

type loggedResponseWriter struct {
	http.ResponseWriter
	status   int
	apiError error
}

func (writer *loggedResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *loggedResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	if status >= 200 || status == http.StatusSwitchingProtocols {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *loggedResponseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (writer *loggedResponseWriter) FlushError() error {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}
