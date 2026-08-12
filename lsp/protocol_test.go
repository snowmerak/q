package lsp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var stream bytes.Buffer
	message := wireMessage{JSONRPC: "2.0", ID: []byte("7"), Method: "textDocument/hover", Params: []byte(`{"line":3}`)}
	if err := writeFrame(&stream, message, 0); err != nil {
		t.Fatal(err)
	}
	body, err := readFrame(bufio.NewReader(&stream), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, `"method":"textDocument/hover"`) || !strings.Contains(got, `"id":7`) {
		t.Fatalf("body = %s", got)
	}
}

func TestReadFrameValidatesHeadersAndBounds(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		maximum int
		want    error
	}{
		{name: "missing length", input: "Content-Type: application/json\r\n\r\n{}", want: errMissingContentLength},
		{name: "invalid length", input: "Content-Length: nope\r\n\r\n", want: errors.New("invalid Content-Length")},
		{name: "too large", input: "Content-Length: 3\r\n\r\nabc", maximum: 2, want: errors.New("exceeds 2 bytes")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(strings.NewReader(test.input)), test.maximum)
			if test.want == errMissingContentLength {
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type shortWriter struct {
	buffer bytes.Buffer
}

func (w *shortWriter) Write(value []byte) (int, error) {
	if len(value) > 2 {
		value = value[:2]
	}
	return w.buffer.Write(value)
}

func TestWriteFrameHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{}
	if err := writeFrame(writer, map[string]string{"jsonrpc": "2.0"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrame(bufio.NewReader(bytes.NewReader(writer.buffer.Bytes())), 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
