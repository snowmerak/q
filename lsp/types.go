package lsp

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	ParseErrorCode     = -32700
	InvalidRequestCode = -32600
	MethodNotFoundCode = -32601
	InvalidParamsCode  = -32602
	InternalErrorCode  = -32603
)

// ResponseError is a JSON-RPC error returned by the language server or a
// Handler. Data is kept raw because LSP methods define their own error data.
type ResponseError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("lsp: server error %d: %s", e.Code, e.Message)
}

// Handler receives requests and notifications initiated by the language
// server. Request results must be JSON-marshalable. Returning a ResponseError
// preserves its JSON-RPC code; other errors become InternalError responses.
type Handler interface {
	Request(context.Context, string, json.RawMessage) (any, error)
	Notification(context.Context, string, json.RawMessage)
}

// HandlerFuncs is a convenience adapter. An omitted request function returns
// MethodNotFound; an omitted notification function ignores notifications.
type HandlerFuncs struct {
	RequestFunc      func(context.Context, string, json.RawMessage) (any, error)
	NotificationFunc func(context.Context, string, json.RawMessage)
}

func (h HandlerFuncs) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if h.RequestFunc == nil {
		return nil, &ResponseError{Code: MethodNotFoundCode, Message: "method not found: " + method}
	}
	return h.RequestFunc(ctx, method, params)
}

func (h HandlerFuncs) Notification(ctx context.Context, method string, params json.RawMessage) {
	if h.NotificationFunc != nil {
		h.NotificationFunc(ctx, method, params)
	}
}

type DocumentURI string

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeParams contains the stable, commonly used portion of the LSP
// initialize request. Capabilities accepts any JSON-marshalable capability
// object so the transport does not need to mirror the entire evolving spec.
type InitializeParams struct {
	ProcessID             *int           `json:"processId"`
	ClientInfo            *ClientInfo    `json:"clientInfo,omitempty"`
	Locale                string         `json:"locale,omitempty"`
	RootPath              *string        `json:"rootPath,omitempty"`
	RootURI               *DocumentURI   `json:"rootUri,omitempty"`
	InitializationOptions any            `json:"initializationOptions,omitempty"`
	Capabilities          map[string]any `json:"capabilities"`
	Trace                 string         `json:"trace,omitempty"`
	WorkspaceFolders      any            `json:"workspaceFolders,omitempty"`
}

type InitializeResult struct {
	Capabilities json.RawMessage `json:"capabilities"`
	ServerInfo   *ServerInfo     `json:"serverInfo,omitempty"`
}

type TextDocumentIdentifier struct {
	URI DocumentURI `json:"uri"`
}

type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}
