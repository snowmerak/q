package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func randomID(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func runServer(ctx context.Context, options appOptions, serve serveOptions) (resultErr error) {
	token, err := randomID(32)
	if err != nil {
		return err
	}
	instance, err := randomID(6)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.State), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(options.State, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("reserve session file (a session may already exist): %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close(), removeOwnedState(options.State, os.Getpid(), instance))
	}()
	d := discovery{Version: protocolVersion, Token: token, Instance: instance, PID: os.Getpid(), EndpointHash: endpointHash(serve.Endpoint), Chrome: serve.Chrome, Headless: serve.Headless, InputMode: serve.InputMode, CommandTimeout: serve.CommandTimeout, CleanupTimeout: serve.CleanupTimeout}
	if options.Named {
		d.Session = options.Session
	}
	if err := json.NewEncoder(file).Encode(d); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	startup, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	var owned *launchedBrowser
	var browserExited <-chan struct{}
	if serve.Chrome != "" {
		owned, err = launchBrowser(startup, options, serve)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, owned.close()) }()
		serve.Endpoint = owned.endpoint
		d.EndpointHash = endpointHash(serve.Endpoint)
		browserExited = owned.done
	}
	sdkOptions := qdriver.Options{CommandTimeout: serve.CommandTimeout, CleanupTimeout: serve.CleanupTimeout}
	if serve.InputMode == "fast" {
		sdkOptions.InputTiming = &qdriver.InputTiming{}
	}
	b, err := qdriver.Attach(startup, serve.Endpoint, sdkOptions)
	if err != nil {
		cancel()
		return errors.New(strings.ReplaceAll(err.Error(), serve.Endpoint, "[browser endpoint]"))
	}
	s, err := newService(startup, b, instance)
	if err == nil {
		err = startup.Err()
	}
	cancel()
	if err != nil {
		_, closeErr := b.Close(context.Background())
		return errors.Join(err, closeErr)
	}
	defer func() { _, err := s.close(); resultErr = errors.Join(resultErr, err) }()
	s.ownedBrowser = owned
	s.inputMode = serve.InputMode
	if options.Named {
		s.sessionID = options.Session
		store, err := newSessionStore(options, serve, instance)
		if err != nil {
			return err
		}
		s.store = store
		if owned != nil {
			store.metadata.BrowserPID = owned.cmd.Process.Pid
			store.metadata.BrowserProfile = owned.profile
		}
		if err := s.persistInventory(); err != nil {
			return err
		}
	}
	d.Address = listener.Addr().String()
	server := &http.Server{Handler: rpcHandler(s, d), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10, IdleTimeout: 30 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(d); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-s.done:
	case <-browserExited:
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	_, closeErr := s.close()
	drain, stop := context.WithTimeout(context.Background(), serve.CleanupTimeout+time.Second)
	defer stop()
	shutdownErr := server.Shutdown(drain)
	return errors.Join(closeErr, shutdownErr)
}

func removeOwnedState(path string, pid int, instance string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var d discovery
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("cannot verify session file ownership: %w", err)
		}
		if d.PID != pid || (instance != "" && d.Instance != instance) {
			return nil
		}
		err = os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// A Windows client can briefly hold the discovery file open while
		// polling shutdown. Recheck ownership before each bounded delete retry.
		if time.Now().After(deadline) {
			return fmt.Errorf("remove owned session file: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
}

func rpcHandler(s *service, d discovery) http.Handler {
	admission := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(status int, value response) {
			data, err := json.Marshal(value)
			if err != nil || len(data) > maxMessage {
				status = http.StatusInternalServerError
				data, _ = json.Marshal(response{OK: false, Error: &commandError{Code: "response_limit", Message: "result could not be encoded within 16 MiB; command is not replayed", MayHaveEffects: true}})
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_, _ = w.Write(append(data, '\n'))
		}
		if r.Host != d.Address || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+d.Token)) != 1 {
			reply(http.StatusUnauthorized, response{Error: &commandError{Code: "unauthorized", Message: "local session authentication failed"}})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
			reply(http.StatusNotFound, response{Error: &commandError{Code: "not_found", Message: "unknown local RPC route"}})
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			reply(http.StatusUnsupportedMediaType, response{Error: &commandError{Code: "invalid_argument", Message: "use application/json"}})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var input request
		if err := dec.Decode(&input); err != nil {
			reply(http.StatusBadRequest, response{Error: failure(invalid("invalid request JSON"), false)})
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			reply(http.StatusBadRequest, response{Error: failure(invalid("request must contain one JSON object"), false)})
			return
		}
		if input.TimeoutMS < 1 || input.TimeoutMS > int64((24*time.Hour)/time.Millisecond) || input.DurationMS < 0 || input.DurationMS > int64((24*time.Hour)/time.Millisecond) {
			reply(http.StatusBadRequest, response{Error: failure(invalid("invalid request duration"), false)})
			return
		}
		// Stop and health remain available while long event waits fill admission.
		if input.Command != "stop" && input.Command != "status" {
			select {
			case admission <- struct{}{}:
				defer func() { <-admission }()
			default:
				reply(http.StatusTooManyRequests, response{Error: &commandError{Code: "busy", Message: "64 commands are already in flight"}})
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(input.TimeoutMS)*time.Millisecond)
		defer cancel()
		result, err := s.run(ctx, input)
		out := response{OK: err == nil, Result: result}
		if err != nil {
			out.Error = failure(err, false)
		}
		reply(http.StatusOK, out)
	})
}
