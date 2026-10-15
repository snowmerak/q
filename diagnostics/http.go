package diagnostics

import (
	"log"
	"log/slog"
)

// HTTPErrorLogger avoids persisting net/http's raw panic values, stack dumps,
// and connection messages. Request middleware records safe correlation fields.
func HTTPErrorLogger(logger *slog.Logger) *log.Logger {
	return log.New(httpErrorWriter{logger: logger}, "", 0)
}

type httpErrorWriter struct{ logger *slog.Logger }

func (w httpErrorWriter) Write(body []byte) (int, error) {
	w.logger.Error("studio.http.failed", "reason_code", "http_server_error")
	return len(body), nil
}
