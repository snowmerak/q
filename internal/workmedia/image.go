package workmedia

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	MaxImages     = 4
	MaxImageBytes = 4 << 20
	MaxTotalBytes = 8 << 20
	MaxImageJSON  = MaxTotalBytes*4/3 + (128 << 10)
)

type Image struct {
	Data []byte `json:"data"`
}

func (i Image) MIME() string { return http.DetectContentType(i.Data) }

func Supported(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/webp" || mime == "image/gif"
}

func Validate(images []Image) error {
	if len(images) > MaxImages {
		return fmt.Errorf("attach up to %d images", MaxImages)
	}
	total := 0
	for _, image := range images {
		total += len(image.Data)
		if len(image.Data) > MaxImageBytes || total > MaxTotalBytes {
			return fmt.Errorf("images must be at most 4 MiB each and 8 MiB combined")
		}
		if !Supported(image.MIME()) {
			return fmt.Errorf("attach a PNG, JPEG, WebP, or GIF image")
		}
	}
	return nil
}

func (i Image) URL() string {
	return "data:" + i.MIME() + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}

func Message(text string, images []Image) client.Message {
	message := client.Message{Role: client.RoleUser, Content: text}
	if len(images) != 0 {
		message.ContentParts = []client.MessageContentPart{{"type": "text", "text": text}}
		for _, image := range images {
			message.ContentParts = append(message.ContentParts, client.MessageContentPart{
				"type": "image_url", "image_url": map[string]any{"url": image.URL()},
			})
		}
	}
	return message
}

func URLs(message client.Message) []string {
	var images []string
	for _, part := range message.ContentParts {
		if part["type"] != "image_url" {
			continue
		}
		value, _ := part["image_url"].(map[string]any)
		url, _ := value["url"].(string)
		header, data, ok := strings.Cut(url, ",")
		mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if ok && strings.HasPrefix(header, "data:") && strings.HasSuffix(header, ";base64") && Supported(mime) && data != "" {
			images = append(images, url)
		}
	}
	return images
}
