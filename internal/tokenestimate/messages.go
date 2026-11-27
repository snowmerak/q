package tokenestimate

import (
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"maps"
	"slices"
	"strings"

	llmprovider "github.com/snowmerak/llm-provider"
	_ "golang.org/x/image/webp"
)

// Messages estimates text from its wire JSON and images from their dimensions.
func Messages(messages []llmprovider.Message) int {
	if len(messages) == 0 {
		return 0
	}
	var estimatedMessages []llmprovider.Message
	var imageTokens int
	for index, message := range messages {
		var estimatedParts []llmprovider.MessageContentPart
		for partIndex, part := range message.ContentParts {
			estimated, tokens := estimateImagePart(part)
			if tokens == 0 {
				continue
			}
			if estimatedParts == nil {
				estimatedParts = slices.Clone(message.ContentParts)
			}
			estimatedParts[partIndex] = estimated
			imageTokens += tokens
		}
		if estimatedParts != nil {
			if estimatedMessages == nil {
				estimatedMessages = slices.Clone(messages)
			}
			estimatedMessages[index].ContentParts = estimatedParts
		}
	}
	if estimatedMessages != nil {
		messages = estimatedMessages
	}
	body, err := json.Marshal(messages)
	if err != nil {
		return len(messages)*8 + imageTokens
	}
	return (len(body)+2)/3 + len(messages)*4 + imageTokens
}

func estimateImagePart(part llmprovider.MessageContentPart) (llmprovider.MessageContentPart, int) {
	kind, _ := part["type"].(string)
	if kind != "image_url" && kind != "input_image" {
		return nil, 0
	}
	url, _ := part["image_url"].(string)
	imageURL, _ := part["image_url"].(map[string]any)
	if imageURL != nil {
		url, _ = imageURL["url"].(string)
	}
	fileID, _ := part["file_id"].(string)
	if url == "" && fileID == "" {
		return nil, 0
	}
	estimated := maps.Clone(part)
	// ACP replay metadata can contain the original image. Only the image URL is sent.
	delete(estimated, "q_acp_content_block")
	if url != "" {
		if imageURL == nil {
			estimated["image_url"] = ""
		} else {
			estimatedURL := maps.Clone(imageURL)
			estimatedURL["url"] = ""
			estimated["image_url"] = estimatedURL
		}
	}
	if fileID != "" {
		estimated["file_id"] = ""
	}
	return estimated, estimateImageTokens(url)
}

func estimateImageTokens(url string) int {
	// An opaque reference or unreadable header gets a fixed high-detail estimate.
	const fallbackTokens = 3000
	if !strings.HasPrefix(url, "data:image/") {
		return fallbackTokens
	}
	header, data, ok := strings.Cut(url, ",")
	if !ok || !strings.HasSuffix(header, ";base64") {
		return fallbackTokens
	}
	const headerReadLimit = 256 << 10
	prefix := io.LimitReader(strings.NewReader(data), headerReadLimit)
	config, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, prefix))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return fallbackTokens
	}
	// Model resizing varies. Use source dimensions with the documented 32-pixel
	// patch size, a 30,000-patch ceiling and a 1.2 token multiplier as a heuristic.
	const patchSize = 32
	const maxPatches = 30_000
	widthPatches := min(maxPatches, (config.Width-1)/patchSize+1)
	heightPatches := min(maxPatches, (config.Height-1)/patchSize+1)
	patches := min(maxPatches, widthPatches*heightPatches)
	return (patches*6 + 4) / 5
}
