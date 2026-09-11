package usagelog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/worklock"
)

const maximumEventBody = 16 << 10

type leader struct {
	server *http.Server
	store  *Store
	lock   *worklock.Lock
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

func startLeader(parent context.Context, dir string, listener net.Listener, lock *worklock.Lock) (*leader, error) {
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	generation, err := randomID()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	health := Health{
		Service: ServiceName, ProtocolVersion: ProtocolVersion,
		Implementation: Implementation, Generation: generation, Ready: true,
	}
	ctx, cancel := context.WithCancel(parent)
	l := &leader{store: store, lock: lock, cancel: cancel, done: make(chan struct{})}
	handler := newHTTPHandler(store, health, listener.Addr().String())
	l.server = &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		maintenance(ctx, store)
	}()
	go func() {
		serveErr := l.server.Serve(listener)
		cancel()
		l.wg.Wait()
		storeErr := l.store.Close()
		lockErr := l.lock.Close()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		l.errMu.Lock()
		l.err = errors.Join(serveErr, storeErr, lockErr)
		l.errMu.Unlock()
		close(l.done)
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := l.server.Shutdown(shutdown); err != nil {
			_ = l.server.Close()
		}
	}()
	return l, nil
}

func maintenance(ctx context.Context, store *Store) {
	_, _ = store.ArchiveExpired(ctx)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = store.ArchiveExpired(ctx)
		}
	}
}

func (l *leader) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() { l.cancel() })
	<-l.done
	l.errMu.Lock()
	defer l.errMu.Unlock()
	return l.err
}

func (l *leader) Done() <-chan struct{} { return l.done }

func newHTTPHandler(store *Store, health Health, allowedHost string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, health)
	})
	mux.HandleFunc("POST /v1/events", func(writer http.ResponseWriter, request *http.Request) {
		if !sameOriginOrAbsent(request) {
			writeProblem(writer, http.StatusForbidden, "cross_origin", "cross-origin writes are not allowed")
			return
		}
		if media := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0])); media != "application/json" {
			writeProblem(writer, http.StatusUnsupportedMediaType, "content_type", "Content-Type must be application/json")
			return
		}
		var record client.UsageRecord
		if err := decodeStrictJSON(writer, request, &record); err != nil {
			writeProblem(writer, http.StatusBadRequest, "invalid_event", err.Error())
			return
		}
		inserted, err := store.Append(request.Context(), record)
		if err != nil {
			writeProblem(writer, http.StatusBadRequest, "invalid_event", err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"inserted": inserted})
	})
	mux.HandleFunc("GET /api/v1/usage", func(writer http.ResponseWriter, request *http.Request) {
		filter, err := parseFilter(request.URL.Query())
		if err != nil {
			writeProblem(writer, http.StatusBadRequest, "invalid_query", err.Error())
			return
		}
		view, err := store.Query(request.Context(), filter)
		if err != nil {
			writeProblem(writer, http.StatusBadRequest, "invalid_query", err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, view)
	})
	mux.HandleFunc("GET /openapi.json", serveOpenAPI)
	mux.HandleFunc("GET /assets/dashboard.css", serveDashboardCSS)
	mux.HandleFunc("GET /assets/dashboard.js", serveDashboardJS)
	mux.HandleFunc("GET /", serveDashboard)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer.Header())
		if request.Host != allowedHost {
			writeProblem(writer, http.StatusMisdirectedRequest, "host", "request Host does not match the loopback listener")
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

func parseFilter(query url.Values) (Filter, error) {
	for key := range query {
		switch key {
		case "from", "to", "model", "role":
		default:
			return Filter{}, fmt.Errorf("unknown query parameter %q", key)
		}
		if len(query[key]) != 1 {
			return Filter{}, fmt.Errorf("query parameter %q must appear once", key)
		}
	}
	var filter Filter
	var err error
	if value := query.Get("from"); value != "" {
		filter.From, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return Filter{}, errors.New("from must be an RFC3339 timestamp")
		}
	}
	if value := query.Get("to"); value != "" {
		filter.To, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return Filter{}, errors.New("to must be an RFC3339 timestamp")
		}
	}
	filter.Model = strings.TrimSpace(query.Get("model"))
	filter.Role = strings.TrimSpace(query.Get("role"))
	if len(filter.Model) > 256 || len(filter.Role) > 64 {
		return Filter{}, errors.New("model or role filter is too long")
	}
	return filter, nil
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumEventBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func sameOriginOrAbsent(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == request.Host && parsed.Path == ""
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cache-Control", "no-store")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("usage: generate ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
