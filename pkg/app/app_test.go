package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/app"
	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/web"
)

func TestAppLifecycleEndpointsAndSingleInstanceLock(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer func() { _ = a.Close(ctx) }()

	// Second Open on the same data directory must be rejected by instance.lock.
	if _, err := app.Open(ctx, app.Options{DataDir: dataDir}); !errors.Is(err, fsutil.ErrLocked) {
		t.Fatalf("expected fsutil.ErrLocked on duplicate Open, got %v", err)
	}

	h := a.Handler()

	// GET /api/v1/health
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	// GET /api/v1/config
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get config status = %d", rec.Code)
	}

	// PUT /api/v1/config
	cfg := config.Default()
	cfg.Tools.ApprovalMode = "request"
	body, _ := json.Marshal(cfg)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put config status = %d body=%s", rec.Code, rec.Body.String())
	}

	// GET /api/v1/models and /api/v1/decisions/models
	for _, path := range []string{"/api/v1/models", "/api/v1/decisions/models"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	// Unmatched /api/ route returns JSON 404, not HTML.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != "not_found" {
		t.Fatalf("expected JSON not_found error, got %s", rec.Body.String())
	}
}

func TestAppEndToEndRoomTurnsSSEMediaAndReindex(t *testing.T) {
	var holdStream atomic.Bool
	streamHeld := make(chan struct{}, 2)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"m1","context_length":16384}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if holdStream.Load() {
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"holding...\"}}]}\n\n")
				flusher.Flush()
				streamHeld <- struct{}{}
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello from \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hana!\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID:      "mock",
		Type:    "openai-compatible",
		Enabled: true,
		BaseURL: upstream.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/m1"
	cfg.LLM.Roles.MemoryModel = "mock/m1"
	if err := config.Save(filepath.Join(dataDir, config.FileName), cfg); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	doJSON := func(method, path string, payload any, out any) int {
		var body io.Reader
		if payload != nil {
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("decode %s %s (%d): %v body=%s", method, path, resp.StatusCode, err, string(raw))
			}
		} else if resp.StatusCode >= 400 {
			t.Logf("%s %s -> %d body=%s", method, path, resp.StatusCode, string(raw))
		}
		return resp.StatusCode
	}

	// 1. Persona CRUD + optimistic locking 409
	var persona domain.Persona
	if code := doJSON(http.MethodPost, "/api/v1/personas", map[string]any{"name": "Kai", "is_default": true}, &persona); code != http.StatusCreated {
		t.Fatalf("create persona = %d", code)
	}
	if code := doJSON(http.MethodPatch, "/api/v1/personas/"+persona.ID, map[string]any{"expected_version": 99, "name": "Conflict"}, nil); code != http.StatusConflict {
		t.Fatalf("expected 409 on stale persona version (id=%q), got %d", persona.ID, code)
	}
	if code := doJSON(http.MethodPatch, "/api/v1/personas/"+persona.ID, map[string]any{"expected_version": persona.Version, "name": "Kai Updated"}, &persona); code != http.StatusOK || persona.Version != 2 {
		t.Fatalf("patch persona = %d v=%d", code, persona.Version)
	}

	// 2. Character CRUD + optimistic locking 409
	var char domain.Character
	if code := doJSON(http.MethodPost, "/api/v1/characters", map[string]any{
		"name":           "Hana",
		"personality":    "Warm cafe owner",
		"initial_values": []string{"Greet guests kindly."},
	}, &char); code != http.StatusCreated {
		t.Fatalf("create char = %d", code)
	}
	if code := doJSON(http.MethodPatch, "/api/v1/characters/"+char.ID, map[string]any{"expected_version": 99, "name": "Conflict"}, nil); code != http.StatusConflict {
		t.Fatalf("expected 409 on stale char version, got %d", code)
	}

	// 3. Create Room
	var roomDetail struct {
		Room    domain.Room         `json:"room"`
		Members []domain.RoomMember `json:"members"`
	}
	if code := doJSON(http.MethodPost, "/api/v1/rooms", map[string]any{
		"title":         "Cafe",
		"mode":          "direct",
		"persona_id":    persona.ID,
		"character_ids": []string{char.ID},
	}, &roomDetail); code != http.StatusCreated {
		t.Fatalf("create room = %d", code)
	}

	// 4. Open SSE stream and submit turn
	sseCtx, cancelSSE := context.WithCancel(ctx)
	defer cancelSSE()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, srv.URL+"/api/v1/rooms/"+roomDetail.Room.ID+"/stream", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK || !strings.HasPrefix(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected SSE headers status=%d ct=%q", sseResp.StatusCode, sseResp.Header.Get("Content-Type"))
	}

	eventsCh := make(chan string, 32)
	var sseWG sync.WaitGroup
	sseWG.Add(1)
	go func() {
		defer sseWG.Done()
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				eventsCh <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()

	var turnResp map[string]any
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "turn-req-1",
		"text":       "Good morning, Hana!",
	}, &turnResp); code != http.StatusAccepted {
		t.Fatalf("submit turn = %d body=%v", code, turnResp)
	}

	seenCompleted := false
	deadline := time.After(5 * time.Second)
	for !seenCompleted {
		select {
		case ev := <-eventsCh:
			if ev == "turn.completed" {
				seenCompleted = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for SSE turn.completed")
		}
	}

	// Idempotency checks
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "turn-req-1",
		"text":       "Good morning, Hana!",
	}, &turnResp); code != http.StatusOK {
		t.Fatalf("idempotent completed turn = %d, want 200", code)
	}
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "turn-req-1",
		"text":       "Changed text!",
	}, nil); code != http.StatusConflict {
		t.Fatalf("idempotency mismatch = %d, want 409", code)
	}

	// Verify persisted messages
	var msgList struct {
		Items []domain.Message `json:"items"`
	}
	if code := doJSON(http.MethodGet, "/api/v1/rooms/"+roomDetail.Room.ID+"/messages?limit=10", nil, &msgList); code != http.StatusOK || len(msgList.Items) != 2 {
		t.Fatalf("list messages = %d len=%d", code, len(msgList.Items))
	}
	if msgList.Items[1].Content != "Hello from Hana!" {
		t.Fatalf("reply = %q", msgList.Items[1].Content)
	}

	// Verify memories endpoint
	if code := doJSON(http.MethodGet, "/api/v1/rooms/"+roomDetail.Room.ID+"/memories", nil, nil); code != http.StatusOK {
		t.Fatalf("room memories = %d", code)
	}

	// 5. Intercept Stop over HTTP
	holdStream.Store(true)
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "turn-req-stop",
		"text":       "Wait...",
	}, nil); code != http.StatusAccepted {
		t.Fatalf("submit stoppable turn = %d", code)
	}
	<-streamHeld
	var interceptResp map[string]any
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/intercept", map[string]any{"text": ""}, &interceptResp); code != http.StatusAccepted || interceptResp["accepted"] != true {
		t.Fatalf("intercept stop = %d %v", code, interceptResp)
	}
	seenStopDone := false
	deadline = time.After(5 * time.Second)
	for !seenStopDone {
		select {
		case ev := <-eventsCh:
			if ev == "turn.completed" {
				seenStopDone = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn.completed after stop")
		}
	}
	cancelSSE()
	sseWG.Wait()

	// 6. Media Upload, Binary Fetch, and Artifact Range Read
	var mpBuf bytes.Buffer
	mpWriter := multipart.NewWriter(&mpBuf)
	part, err := mpWriter.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("0123456789ABCDEF"))
	_ = mpWriter.Close()

	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/media", &mpBuf)
	upReq.Header.Set("Content-Type", mpWriter.FormDataContentType())
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload media = %d", upResp.StatusCode)
	}
	var uploaded struct {
		ArtifactID string `json:"artifact_id"`
		URL        string `json:"url"`
	}
	_ = json.NewDecoder(upResp.Body).Decode(&uploaded)

	dlResp, err := http.Get(srv.URL + uploaded.URL)
	if err != nil {
		t.Fatal(err)
	}
	dlBytes, _ := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	if string(dlBytes) != "0123456789ABCDEF" {
		t.Fatalf("downloaded media = %q", string(dlBytes))
	}

	var rangeResp struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if code := doJSON(http.MethodGet, "/api/v1/artifacts/"+uploaded.ArtifactID+"?offset=4&limit=6", nil, &rangeResp); code != http.StatusOK || rangeResp.Content != "456789" || !rangeResp.Truncated {
		t.Fatalf("artifact range = %d %+v", code, rangeResp)
	}

	// 7. Reindex endpoint
	var reindexResp map[string]any
	if code := doJSON(http.MethodPost, "/api/v1/storage/reindex", nil, &reindexResp); code != http.StatusAccepted || reindexResp["job_id"] == "" {
		t.Fatalf("reindex = %d %v", code, reindexResp)
	}

	// 8. Knowledge CRUD & Search + Skills list
	var kDoc struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	if code := doJSON(http.MethodPost, "/api/v1/knowledge", map[string]any{
		"scope":   "global",
		"title":   "Protocol Note",
		"content": "Always verify harbor coordinates twice.",
	}, &kDoc); code != http.StatusCreated || kDoc.Revision != 1 {
		t.Fatalf("create knowledge = %d rev=%d", code, kDoc.Revision)
	}
	if code := doJSON(http.MethodPatch, "/api/v1/knowledge/"+kDoc.ID, map[string]any{
		"expected_revision": kDoc.Revision,
		"content":           "Updated harbor coordinate checklist.",
	}, &kDoc); code != http.StatusOK || kDoc.Revision != 2 {
		t.Fatalf("patch knowledge = %d rev=%d", code, kDoc.Revision)
	}
	if code := doJSON(http.MethodGet, "/api/v1/knowledge", nil, nil); code != http.StatusOK {
		t.Fatalf("list knowledge = %d", code)
	}
	if code := doJSON(http.MethodPost, "/api/v1/knowledge/search", map[string]any{"query": "checklist"}, nil); code != http.StatusOK {
		t.Fatalf("search knowledge = %d", code)
	}
	if code := doJSON(http.MethodDelete, "/api/v1/knowledge/"+kDoc.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete knowledge = %d", code)
	}
	if code := doJSON(http.MethodGet, "/api/v1/skills", nil, nil); code != http.StatusOK {
		t.Fatalf("list skills = %d", code)
	}
}

func TestEmbeddedSPAServingAndAPI404Isolation(t *testing.T) {
	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: t.TempDir(), ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	h := a.Handler()

	// Unmatched /api/ path must always return JSON 404.
	apiRec := httptest.NewRecorder()
	h.ServeHTTP(apiRec, httptest.NewRequest(http.MethodGet, "/api/v1/unknown-route", nil))
	if apiRec.Code != http.StatusNotFound || !strings.Contains(apiRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unexpected /api 404 response: status=%d ct=%q body=%s", apiRec.Code, apiRec.Header().Get("Content-Type"), apiRec.Body.String())
	}

	if _, ok := web.DistFS(); ok {
		for _, p := range []string{"/", "/rooms/demo"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<div id="root">`) {
				t.Fatalf("SPA route %s failed: status=%d body=%s", p, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestExtendedStorageDriversWiring(t *testing.T) {
	var (
		mu      sync.Mutex
		indices = make(map[string]map[string]map[string]any)
		aliases = make(map[string][]string)
		objects = make(map[string][]byte)
	)
	osSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/_alias/"):
			alias := strings.TrimPrefix(r.URL.Path, "/_alias/")
			targets := aliases[alias]
			if len(targets) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			resp := map[string]any{}
			for _, idx := range targets {
				resp[idx] = map[string]any{"aliases": map[string]any{alias: map[string]any{}}}
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/_aliases":
			var req struct {
				Actions []map[string]struct {
					Index string `json:"index"`
					Alias string `json:"alias"`
				} `json:"actions"`
			}
			_ = json.Unmarshal(body, &req)
			for _, act := range req.Actions {
				if add, ok := act["add"]; ok {
					aliases[add.Alias] = append(aliases[add.Alias], add.Index)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})
		case strings.HasSuffix(r.URL.Path, "/_count"):
			_ = json.NewEncoder(w).Encode(map[string]any{"count": 0})
		case r.URL.Path == "/_bulk":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": false})
		case strings.HasSuffix(r.URL.Path, "/_search"):
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{}}})
		case r.Method == http.MethodPut:
			idxName := strings.TrimPrefix(r.URL.Path, "/")
			if indices[idxName] == nil {
				indices[idxName] = map[string]map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer osSrv.Close()

	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			objects[key] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer s3Srv.Close()

	t.Setenv("Q_TEST_S3_AK", "test-ak")
	t.Setenv("Q_TEST_S3_SK", "test-sk")

	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Search.Driver = "opensearch"
	cfg.Storage.Search.OpenSearch.Endpoint = osSrv.URL
	cfg.Storage.Search.OpenSearch.IndexPrefix = "q_app_test"
	cfg.Storage.Blob.Driver = "s3"
	cfg.Storage.Blob.S3.Endpoint = s3Srv.URL
	cfg.Storage.Blob.S3.Bucket = "q-media"
	cfg.Storage.Blob.S3.Region = "us-east-1"
	cfg.Storage.Blob.S3.AccessKeyEnv = "Q_TEST_S3_AK"
	cfg.Storage.Blob.S3.SecretKeyEnv = "Q_TEST_S3_SK"

	rawCfg, _ := json.MarshalIndent(cfg, "", "  ")
	if err := writeTestFile(filepath.Join(dataDir, "config.json"), rawCfg); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open with opensearch+s3: %v", err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	h := a.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d body=%s", rec.Code, rec.Body.String())
	}

	// Upload media through S3 blob driver.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("s3-media-payload"))
	_ = mw.Close()
	upReq := httptest.NewRequest(http.MethodPost, "/api/v1/media", &bodyBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRec := httptest.NewRecorder()
	h.ServeHTTP(upRec, upReq)
	if upRec.Code != http.StatusCreated {
		t.Fatalf("media upload via s3 = %d body=%s", upRec.Code, upRec.Body.String())
	}
	if len(objects) == 0 {
		t.Fatal("expected object uploaded to mock S3 server")
	}
}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func TestAppStartAsyncLoopbackHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	a, err := app.Open(ctx, app.Options{DataDir: t.TempDir(), ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	addr, errCh, err := a.StartAsync(ctx)
	if err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	if !strings.Contains(addr, ":") || strings.HasSuffix(addr, ":0") {
		t.Fatalf("expected resolved port in addr, got %q", addr)
	}

	resp, err := http.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case serveErr := <-errCh:
		if serveErr != nil {
			t.Fatalf("unexpected serve err on cancel: %v", serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for StartAsync shutdown")
	}
}

func TestLoomStatsGCAndConfigHotReloadEndpoints(t *testing.T) {
	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: t.TempDir(), ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	h := a.Handler()

	// Seed one Loom artifact directly and verify /api/v1/storage/loom/stats.
	// Using a fresh room media upload vs LoomStats: stats must ignore media artifacts.
	var statsBefore struct {
		Artifacts       int   `json:"artifacts"`
		Blobs           int   `json:"blobs"`
		Bytes           int64 `json:"bytes"`
		MaxStoreBytes   int64 `json:"max_store_bytes"`
		PinnedArtifacts int   `json:"pinned_artifacts"`
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/storage/loom/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET loom stats = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statsBefore); err != nil {
		t.Fatal(err)
	}
	if statsBefore.MaxStoreBytes != 256<<20 {
		t.Fatalf("expected default MaxStoreBytes=256MiB, got %d", statsBefore.MaxStoreBytes)
	}

	// Dry-run GC endpoint accepts JSON body.
	gcReq := httptest.NewRequest(http.MethodPost, "/api/v1/storage/loom/gc", strings.NewReader(`{"dry_run":true}`))
	gcReq.Header.Set("Content-Type", "application/json")
	gcRec := httptest.NewRecorder()
	h.ServeHTTP(gcRec, gcReq)
	if gcRec.Code != http.StatusOK {
		t.Fatalf("POST loom gc dry_run = %d body=%s", gcRec.Code, gcRec.Body.String())
	}
	var gcRes struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(gcRec.Body.Bytes(), &gcRes); err != nil || !gcRes.DryRun {
		t.Fatalf("unexpected dry_run response: %s err=%v", gcRec.Body.String(), err)
	}

	// PUT /api/v1/config updating loom_gc hot-reloads without restart_required_fields.
	cfg := config.Default()
	cfg.Storage.Blob.LoomMaxStoreBytes = 128 << 20
	cfg.Storage.Blob.LoomGC.Disabled = true
	cfg.Storage.Blob.LoomGC.TriggerRatio = 0.90
	cfg.Storage.Blob.LoomGC.TargetRatio = 0.50
	cfg.Storage.Blob.LoomGC.GraceHours = 3
	cfgBytes, _ := json.Marshal(cfg)

	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(cfgBytes))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT config = %d body=%s", putRec.Code, putRec.Body.String())
	}
	var putResp struct {
		Applied               bool     `json:"applied"`
		RestartRequiredFields []string `json:"restart_required_fields"`
	}
	if err := json.Unmarshal(putRec.Body.Bytes(), &putResp); err != nil {
		t.Fatal(err)
	}
	if !putResp.Applied || len(putResp.RestartRequiredFields) != 0 {
		t.Fatalf("expected hot-reload with no restart required, got %+v", putResp)
	}

	// Verify stats reflect updated MaxStoreBytes immediately.
	recAfter := httptest.NewRecorder()
	h.ServeHTTP(recAfter, httptest.NewRequest(http.MethodGet, "/api/v1/storage/loom/stats", nil))
	if err := json.Unmarshal(recAfter.Body.Bytes(), &statsBefore); err != nil {
		t.Fatal(err)
	}
	if statsBefore.MaxStoreBytes != 128<<20 {
		t.Fatalf("expected hot-reloaded MaxStoreBytes=128MiB, got %d", statsBefore.MaxStoreBytes)
	}
}

func TestRoomWorkDirCreatePatchAndClear(t *testing.T) {
	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: t.TempDir(), ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	h := a.Handler()
	doJSON := func(method, path string, body any, out any) int {
		var rBody io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rBody = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rBody)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if out != nil && rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), out)
		}
		return rec.Code
	}

	var persona struct {
		ID string `json:"id"`
	}
	if code := doJSON(http.MethodPost, "/api/v1/personas", map[string]any{"name": "User"}, &persona); code != http.StatusCreated {
		t.Fatalf("create persona = %d", code)
	}
	var char struct {
		ID string `json:"id"`
	}
	if code := doJSON(http.MethodPost, "/api/v1/characters", map[string]any{"name": "Kai"}, &char); code != http.StatusCreated {
		t.Fatalf("create char = %d", code)
	}

	type roomDetailDTO struct {
		Room struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
			WorkDir string `json:"work_dir"`
		} `json:"room"`
	}
	var detail roomDetailDTO
	if code := doJSON(http.MethodPost, "/api/v1/rooms", map[string]any{
		"title":         "Project Room",
		"mode":          "direct",
		"persona_id":    persona.ID,
		"character_ids": []string{char.ID},
		"work_dir":      "  C:\\work\\alpha  ",
	}, &detail); code != http.StatusCreated || detail.Room.WorkDir != `C:\work\alpha` {
		t.Fatalf("create room work_dir = %d %+v", code, detail)
	}

	var patched roomDetailDTO
	if code := doJSON(http.MethodPatch, "/api/v1/rooms/"+detail.Room.ID, map[string]any{
		"expected_version": detail.Room.Version,
		"work_dir":         "/tmp/beta",
	}, &patched); code != http.StatusOK || patched.Room.WorkDir != "/tmp/beta" {
		t.Fatalf("patch room work_dir = %d %+v", code, patched)
	}

	var cleared roomDetailDTO
	if code := doJSON(http.MethodPatch, "/api/v1/rooms/"+detail.Room.ID, map[string]any{
		"expected_version": patched.Room.Version,
		"work_dir":         "",
	}, &cleared); code != http.StatusOK || cleared.Room.WorkDir != "" {
		t.Fatalf("clear room work_dir = %d %+v", code, cleared)
	}
}

func TestMessagesToolInvocationsAndCompactionFiltering(t *testing.T) {
	workDir := t.TempDir()
	if err := writeTestFile(filepath.Join(workDir, "note.txt"), []byte("secret note")); err != nil {
		t.Fatal(err)
	}

	var streamRound atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"m1","context_length":16384}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream   bool `json:"stream"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream {
				w.Header().Set("Content-Type", "application/json")
				if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "running memory summary") {
					_, _ = io.WriteString(w, `{"id":"sum1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"Compacted earlier tool turn."},"finish_reason":"stop"}]}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			hasToolResult := false
			for _, m := range req.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, "secret note") {
					hasToolResult = true
					break
				}
			}
			switch {
			case streamRound.Add(1) == 1:
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first inspect note","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"note.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			case hasToolResult && !strings.Contains(string(body), "Compaction trigger"):
				_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>verified note content</think>Read note: secret note."},"finish_reason":"stop"}]}`+"\n\n")
			default:
				_, _ = io.WriteString(w, `data: {"id":"c3","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"turn 2 thought","content":"Compaction turn complete."},"finish_reason":"stop"}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID:      "mock",
		Type:    "openai-compatible",
		Enabled: true,
		BaseURL: upstream.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/m1"
	cfg.LLM.Roles.MemoryModel = "mock/m1"
	if err := config.Save(filepath.Join(dataDir, config.FileName), cfg); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	doJSON := func(method, path string, payload any, out any) int {
		var body io.Reader
		if payload != nil {
			b, _ := json.Marshal(payload)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, body)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("decode %s: %v body=%s", path, err, string(raw))
			}
		}
		return resp.StatusCode
	}

	var persona domain.Persona
	if code := doJSON(http.MethodPost, "/api/v1/personas", map[string]any{"name": "Alex", "is_default": true}, &persona); code != http.StatusCreated {
		t.Fatalf("create persona = %d", code)
	}
	var char domain.Character
	if code := doJSON(http.MethodPost, "/api/v1/characters", map[string]any{
		"name":        "Hana",
		"model_prefs": map[string]any{"context_window_override": 400},
	}, &char); code != http.StatusCreated {
		t.Fatalf("create character = %d", code)
	}
	var roomDetail struct {
		Room domain.Room `json:"room"`
	}
	if code := doJSON(http.MethodPost, "/api/v1/rooms", map[string]any{
		"title":                  "Tool Room",
		"mode":                   "direct",
		"persona_id":             persona.ID,
		"character_ids":          []string{char.ID},
		"approval_mode_override": "bypass",
		"work_dir":               workDir,
	}, &roomDetail); code != http.StatusCreated {
		t.Fatalf("create room = %d", code)
	}

	sseCtx, cancelSSE := context.WithCancel(ctx)
	defer cancelSSE()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, srv.URL+"/api/v1/rooms/"+roomDetail.Room.ID+"/stream", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	eventsCh := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "event: ") {
				eventsCh <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()
	waitTurnCompleted := func() {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-eventsCh:
				if ev == "turn.completed" {
					return
				}
			case <-deadline:
				t.Fatal("timed out waiting for turn.completed")
			}
		}
	}

	// Turn 1: tool call + thinking before compaction
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "t1",
		"text":       "Read note.txt",
	}, nil); code != http.StatusAccepted {
		t.Fatalf("turn 1 = %d", code)
	}
	waitTurnCompleted()

	var beforeCompact struct {
		Items           []domain.Message        `json:"items"`
		ToolInvocations []domain.ToolInvocation `json:"tool_invocations"`
	}
	if code := doJSON(http.MethodGet, "/api/v1/rooms/"+roomDetail.Room.ID+"/messages?limit=20", nil, &beforeCompact); code != http.StatusOK {
		t.Fatalf("messages before compaction = %d", code)
	}
	if len(beforeCompact.Items) != 2 || len(beforeCompact.ToolInvocations) != 1 || beforeCompact.ToolInvocations[0].ToolName != "read_file" {
		t.Fatalf("expected 2 messages and 1 read_file tool invocation before compaction, got items=%d invs=%+v", len(beforeCompact.Items), beforeCompact.ToolInvocations)
	}
	var meta1 map[string]any
	if err := json.Unmarshal([]byte(beforeCompact.Items[1].MetadataJSON), &meta1); err != nil || meta1["thinking"] != "first inspect note\n\nverified note content" {
		t.Fatalf("expected thinking before compaction, got %q (err=%v)", beforeCompact.Items[1].MetadataJSON, err)
	}

	// Turn 2: long prompt triggers compaction covering Turn 1 (seq 1..2).
	if code := doJSON(http.MethodPost, "/api/v1/rooms/"+roomDetail.Room.ID+"/turns", map[string]any{
		"request_id": "t2",
		"text":       strings.Repeat("Compaction trigger text block ", 35),
	}, nil); code != http.StatusAccepted {
		t.Fatalf("turn 2 = %d", code)
	}
	waitTurnCompleted()

	var afterCompact struct {
		Items           []domain.Message        `json:"items"`
		ToolInvocations []domain.ToolInvocation `json:"tool_invocations"`
	}
	if code := doJSON(http.MethodGet, "/api/v1/rooms/"+roomDetail.Room.ID+"/messages?limit=20", nil, &afterCompact); code != http.StatusOK {
		t.Fatalf("messages after compaction = %d", code)
	}
	if len(afterCompact.ToolInvocations) != 0 {
		t.Fatalf("expected compacted turn 1 tool invocations omitted, got %+v", afterCompact.ToolInvocations)
	}
	if len(afterCompact.Items) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(afterCompact.Items))
	}
	if afterCompact.Items[1].MetadataJSON != "" {
		var stripped map[string]any
		_ = json.Unmarshal([]byte(afterCompact.Items[1].MetadataJSON), &stripped)
		if _, hasThinking := stripped["thinking"]; hasThinking {
			t.Fatalf("expected thinking stripped from compacted message metadata, got %q", afterCompact.Items[1].MetadataJSON)
		}
	}
	var meta2 map[string]any
	if err := json.Unmarshal([]byte(afterCompact.Items[3].MetadataJSON), &meta2); err != nil || meta2["thinking"] != "turn 2 thought" {
		t.Fatalf("expected uncompacted turn 2 thinking preserved, got %q", afterCompact.Items[3].MetadataJSON)
	}
}
