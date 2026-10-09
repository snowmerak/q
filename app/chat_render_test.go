package app

import (
	"github.com/snowmerak/q/client"
	"strings"
)

func renderTranscript(messages []client.Message, width int) string {
	return renderTranscriptWithStyle(messages, width, true)
}

func renderTranscriptWithStyle(messages []client.Message, width int, dark bool) string {
	return renderStreamingTranscriptWithStyle(messages, nil, "", width, dark)
}

func renderStreamingTranscriptWithStyle(
	messages []client.Message,
	thoughts []transcriptThought,
	streamResponse string,
	width int,
	dark bool,
) string {
	return strings.Join(renderTranscriptBlocks(messages, thoughts, streamResponse, width, dark, false), "\n\n")
}
