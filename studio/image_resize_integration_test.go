package studio

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/workmedia"
)

func TestStudioSendsAndStoresTheSmallerImage(t *testing.T) {
	sent := make(chan []string, 2)
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		var request struct {
			Messages []client.Message `json:"messages"`
			Stream   bool             `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if !request.Stream {
			t.Error("expected a streamed model request")
		}
		var images []string
		for _, message := range request.Messages {
			images = append(images, workmedia.URLs(message)...)
		}
		sent <- images
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"image-resize\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Image received.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}, func(value *config.Config) { value.Context.Window = 262144 })
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, 1888, 1056))); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/sessions/" + fixture.session.SessionID
	response := serveJSON(t, fixture.handler, http.MethodPost, base+"/messages", sessionRunRequest{
		WorkspaceRoot: fixture.root, Images: []app.ImageInput{{Data: data.Bytes()}}, ImageMaxEdge: 1600,
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit image = %d %s", response.Code, response.Body)
	}
	var run studioRunSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	endpoint := server.URL + base + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(fixture.root)
	page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" || page.Run.ContextUsed == 0 {
		t.Fatalf("image turn = %+v", page.Run)
	}
	var images []string
	select {
	case images = <-sent:
	default:
		t.Fatal("no model request arrived")
	}
	if len(images) != 1 {
		t.Fatalf("received %d images, want one", len(images))
	}
	_, encoded, _ := strings.Cut(images[0], ",")
	resized, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	dimensions, err := png.DecodeConfig(bytes.NewReader(resized))
	if err != nil || dimensions.Width != 1600 || dimensions.Height != 895 {
		t.Fatalf("provider image = %dx%d, want 1600x895: %v", dimensions.Width, dimensions.Height, err)
	}
	saved, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, history := range [][]client.Message{saved.Transcript, saved.Context} {
		found := false
		for _, message := range history {
			for _, imageURL := range workmedia.URLs(message) {
				found = found || imageURL == images[0]
			}
		}
		if !found {
			t.Fatal("saved history does not contain the image sent to the model")
		}
	}
}
