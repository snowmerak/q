package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPPromptResourcesPreserveProvenanceAndReplay(t *testing.T) {
	link := acp.ResourceLinkBlock("guide.md", "file:///workspace/guide.md")
	link.ResourceLink.MimeType = acp.Ptr("text/markdown")
	link.ResourceLink.Description = acp.Ptr("Project guide")
	textResource := acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
		Uri: "file:///workspace/main.go", MimeType: acp.Ptr("text/x-go"), Text: "package main\n",
	}})
	binaryBody := []byte{0x00, 0x01, 0x02, 0xff}
	binaryResource := acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
		Uri: "file:///workspace/data.bin", MimeType: acp.Ptr("application/octet-stream"),
		Blob: base64.StdEncoding.EncodeToString(binaryBody),
	}})
	blocks := []acp.ContentBlock{acp.TextBlock("inspect these resources"), link, textResource, binaryResource}

	message, nonText, err := acpPromptMessage(blocks, false)
	if err != nil {
		t.Fatal(err)
	}
	if !nonText || len(message.ContentParts) != len(blocks) {
		t.Fatalf("resource prompt = %#v, nonText=%v", message, nonText)
	}
	for index := 1; index < len(message.ContentParts); index++ {
		if _, ok := storedACPContentBlock(message.ContentParts[index]); !ok {
			t.Fatalf("content part %d lost its ACP block: %#v", index, message.ContentParts[index])
		}
	}

	provider := providerMessages([]client.Message{message}, false)[0]
	for index, part := range provider.ContentParts {
		if _, leaked := part[acpContentBlockMetadataKey]; leaked {
			t.Fatalf("provider part %d leaked q ACP metadata: %#v", index, part)
		}
	}
	providerText := provider.TextContent()
	for _, expected := range []string{
		`URI: "file:///workspace/guide.md"`, `MIME-Type: "text/markdown"`,
		`URI: "file:///workspace/main.go"`, "package main", "Encoding: base64",
		base64.StdEncoding.EncodeToString(binaryBody),
	} {
		if !strings.Contains(providerText, expected) {
			t.Fatalf("provider text omitted %q:\n%s", expected, providerText)
		}
	}
	if _, ok := storedACPContentBlock(message.ContentParts[1]); !ok {
		t.Fatal("provider projection mutated the persisted message")
	}

	body, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var restored client.Message
	if err := json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	updates := replayACPUserMessage(restored)
	if len(updates) != len(blocks) {
		t.Fatalf("replay updates = %#v", updates)
	}
	for index, update := range updates {
		if update.UserMessageChunk == nil {
			t.Fatalf("replay update %d is not a user message: %#v", index, update)
		}
		got, err := json.Marshal(update.UserMessageChunk.Content)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(blocks[index])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("replayed block %d = %s, want %s", index, got, want)
		}
	}
}

func TestACPPromptImageResourceKeepsResourceIdentity(t *testing.T) {
	resource := acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
		Uri: "file:///workspace/diagram.png", MimeType: acp.Ptr("image/png"),
		Blob: base64.StdEncoding.EncodeToString([]byte("image bytes")),
	}})
	message, nonText, err := acpPromptMessage([]acp.ContentBlock{resource}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !nonText || len(message.ContentParts) != 1 || message.ContentParts[0]["type"] != "image_url" {
		t.Fatalf("image resource prompt = %#v", message)
	}
	replayed := replayACPUserMessage(message)
	if len(replayed) != 1 || replayed[0].UserMessageChunk == nil || replayed[0].UserMessageChunk.Content.Resource == nil {
		t.Fatalf("image resource replay = %#v", replayed)
	}
	if replayed[0].UserMessageChunk.Content.Resource.Resource.BlobResourceContents.Uri != "file:///workspace/diagram.png" {
		t.Fatalf("image resource identity was lost: %#v", replayed[0])
	}
}

func TestACPFileMentionsBecomeResourcesOrLinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("remember this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary data.bin"), []byte{0x00, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}

	content := `Review @notes.txt and @"binary data.bin" and @notes.txt.`
	blocks, err := acpPromptBlocksForText(root, content, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[1].Resource == nil || blocks[2].Resource == nil {
		t.Fatalf("embedded mention blocks = %#v", blocks)
	}
	if text := blocks[1].Resource.Resource.TextResourceContents; text == nil || text.Text != "remember this\n" || !strings.HasPrefix(text.Uri, "file:///") {
		t.Fatalf("text mention = %#v", blocks[1])
	}
	if blob := blocks[2].Resource.Resource.BlobResourceContents; blob == nil || blob.Blob != base64.StdEncoding.EncodeToString([]byte{0x00, 0xff}) {
		t.Fatalf("binary mention = %#v", blocks[2])
	}

	links, err := acpPromptBlocksForText(root, content, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 || links[1].ResourceLink == nil || links[2].ResourceLink == nil || links[1].ResourceLink.Name != "notes.txt" {
		t.Fatalf("resource link mentions = %#v", links)
	}

	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acpPromptBlocksForText(root, "Review @../outside.txt", true); err == nil || !strings.Contains(err.Error(), "escapes the workspace root") {
		t.Fatalf("outside mention error = %v", err)
	}
}

func TestACPRemoteClientSendsAndRetainsResources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "context.md"), []byte("remote context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := &fakeACPRemoteConnection{}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "session-1", root: root,
		capabilities: acp.AgentCapabilities{PromptCapabilities: acp.PromptCapabilities{EmbeddedContext: true}},
	}
	responseResource := acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
		Uri: "memory://result", MimeType: acp.Ptr("text/markdown"), Text: "# Result\nPreserved\n",
	}})
	connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		if len(request.Prompt) != 2 || request.Prompt[1].Resource == nil {
			t.Fatalf("remote prompt did not embed the mention: %#v", request.Prompt)
		}
		resource := request.Prompt[1].Resource.Resource.TextResourceContents
		if resource == nil || resource.Text != "remote context\n" || !strings.HasPrefix(resource.Uri, "file:///") {
			t.Fatalf("remote prompt resource = %#v", request.Prompt[1])
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: responseResource},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		link := acp.ResourceLinkBlock("artifact.md", "file:///workspace/artifact.md")
		link.ResourceLink.MimeType = acp.Ptr("text/markdown")
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: link},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	response, _, err := remote.prompt(t.Context(), "Use @context.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if len(message.ContentParts) != 2 || !strings.Contains(message.TextContent(), "memory://result") ||
		!strings.Contains(message.TextContent(), "Preserved") || !strings.Contains(message.TextContent(), "artifact.md") {
		t.Fatalf("retained remote response = %#v", message)
	}
	block, ok := storedACPContentBlock(message.ContentParts[0])
	if !ok || block.Resource == nil || block.Resource.Resource.TextResourceContents.Uri != "memory://result" {
		t.Fatalf("remote response lost its resource: %#v", message.ContentParts[0])
	}
	block, ok = storedACPContentBlock(message.ContentParts[1])
	if !ok || block.ResourceLink == nil || block.ResourceLink.Uri != "file:///workspace/artifact.md" {
		t.Fatalf("remote response lost its resource link: %#v", message.ContentParts[1])
	}
}
