package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
)

func TestIntegrationGatewaySendsImageToCodex(t *testing.T) {
	if os.Getenv("Q_GATEWAY_CODEX_IMAGE_INTEGRATION") == "" {
		t.Skip("set Q_GATEWAY_CODEX_IMAGE_INTEGRATION=1 to send a real image through Q Gateway to Codex")
	}
	model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	providerStore := providerhost.Store{Dir: t.TempDir()}
	if err := providerStore.Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "codex", Type: "codex-app-server", Prefix: "codex", Enabled: true, Models: []string{model},
		Codex: gateway.CodexConfig{Model: model},
	}}}); err != nil {
		t.Fatal(err)
	}
	settingsStore := gatewayconfig.Store{Dir: providerStore.Dir}
	ctx, cancel := context.WithCancel(t.Context())
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runGatewayWithStore(ctx, nil, stdoutWriter, io.Discard, providerStore, settingsStore)
		_ = stdoutWriter.Close()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Q Gateway did not stop after cancellation")
		}
		_ = stdoutReader.Close()
	})
	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(strings.TrimPrefix(line, "q gateway listening on "))

	const width, height = 512, 256
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixel := color.RGBA{A: 255}
			if x < width/2 {
				pixel = color.RGBA{R: 255, G: 255, A: 255}
			}
			picture.Set(x, y, pixel)
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, picture); err != nil {
		t.Fatal(err)
	}
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData.Bytes())
	requestBody, err := json.Marshal(client.ChatRequest{
		Model: "codex/" + model,
		Messages: []client.Message{{Role: client.RoleUser, ContentParts: []client.MessageContentPart{
			{"type": "text", "text": "What colors fill the left and right halves of this image? Reply 'left: ..., right: ...'."},
			{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	response, err := httpClient.Post(endpoint+"/chat/completions", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Gateway status = %d: %s", response.StatusCode, data)
	}
	var completion client.ChatResponse
	if err := json.Unmarshal(data, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) == 0 {
		t.Fatalf("Gateway returned no choices: %s", data)
	}
	answer := strings.ToLower(completion.Choices[0].Message.Content)
	if !strings.Contains(answer, "left: yellow") || !strings.Contains(answer, "right: black") {
		t.Fatalf("Codex did not describe the image: %q", answer)
	}
	t.Logf("Codex image answer: %s", answer)
}
