package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	defaultMaximumMessageBytes = 16 << 20
	maximumHeaderBytes         = 16 << 10
)

var errMissingContentLength = errors.New("lsp: message is missing Content-Length")

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

func readFrame(reader *bufio.Reader, maximum int) ([]byte, error) {
	if maximum <= 0 {
		maximum = defaultMaximumMessageBytes
	}
	headerBytes := 0
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		headerBytes += len(line)
		if headerBytes > maximumHeaderBytes {
			return nil, fmt.Errorf("lsp: message headers exceed %d bytes", maximumHeaderBytes)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && headerBytes == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lsp: read message header: %w", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			return nil, fmt.Errorf("lsp: malformed message header %q", line)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		if contentLength >= 0 {
			return nil, errors.New("lsp: duplicate Content-Length header")
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil || parsed < 0 {
			return nil, fmt.Errorf("lsp: invalid Content-Length %q", strings.TrimSpace(value))
		}
		contentLength = parsed
	}
	if contentLength < 0 {
		return nil, errMissingContentLength
	}
	if contentLength > maximum {
		return nil, fmt.Errorf("lsp: message body exceeds %d bytes", maximum)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, fmt.Errorf("lsp: read message body: %w", err)
	}
	return body, nil
}

func writeFrame(writer io.Writer, value any, maximum int) error {
	if maximum <= 0 {
		maximum = defaultMaximumMessageBytes
	}
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("lsp: encode message: %w", err)
	}
	if len(body) > maximum {
		return fmt.Errorf("lsp: message body exceeds %d bytes", maximum)
	}
	header := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	if err := writeAll(writer, []byte(header)); err != nil {
		return fmt.Errorf("lsp: write message header: %w", err)
	}
	if err := writeAll(writer, body); err != nil {
		return fmt.Errorf("lsp: write message body: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}
