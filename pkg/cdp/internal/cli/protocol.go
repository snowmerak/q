package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

const protocolVersion = 1
const maxMessage = 16 << 20
const maxHandles = 1024

type request struct {
	Command    string   `json:"command"`
	Task       string   `json:"task,omitempty"`
	Page       string   `json:"page,omitempty"`
	Name       string   `json:"name,omitempty"`
	ID         string   `json:"id,omitempty"`
	Value      string   `json:"value,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Keep       []string `json:"keep,omitempty"`
	Locator    locator  `json:"locator,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Root       string   `json:"root,omitempty"`
	MaxNodes   int      `json:"maxNodes,omitempty"`
	MaxBytes   int      `json:"maxBytes,omitempty"`
	DurationMS int64    `json:"durationMs,omitempty"`
	TimeoutMS  int64    `json:"timeoutMs"`
	All        bool     `json:"all,omitempty"`
}

type locator struct {
	Kind     string `json:"kind,omitempty"`
	Value    string `json:"value,omitempty"`
	Name     string `json:"name,omitempty"`
	Frame    string `json:"frame,omitempty"`
	FrameSet bool   `json:"frameSet,omitempty"`
}
type response struct {
	OK     bool          `json:"ok"`
	Result any           `json:"result,omitempty"`
	Error  *commandError `json:"error,omitempty"`
}
type commandError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	MayHaveEffects bool   `json:"mayHaveEffects"`
	Operation      string `json:"operation,omitempty"`
	PageID         string `json:"pageId,omitempty"`
}

func (e *commandError) Error() string { return e.Message }

func failure(err error, effects bool) *commandError {
	code := "operation_failed"
	for _, test := range []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "deadline"},
		{qdriver.ErrDialogOpen, "dialog_open"}, {qdriver.ErrDialogStale, "dialog_stale"},
		{qdriver.ErrStaleRef, "stale_ref"}, {qdriver.ErrWrongPage, "wrong_page"},
		{qdriver.ErrAmbiguous, "ambiguous"}, {qdriver.ErrElementNotFound, "element_not_found"},
		{qdriver.ErrNotActionable, "not_actionable"}, {qdriver.ErrNotEditable, "not_editable"},
		{qdriver.ErrInvalidLocator, "invalid_locator"}, {qdriver.ErrNotOwned, "not_owned"},
		{qdriver.ErrPageReleased, "page_released"}, {qdriver.ErrPageNotFound, "page_not_found"},
		{qdriver.ErrTaskFinished, "task_finished"}, {qdriver.ErrPageInUse, "page_in_use"},
		{qdriver.ErrEventOverflow, "event_overflow"}, {qdriver.ErrSubscriptionClosed, "subscription_closed"},
		{qdriver.ErrDownloadsDisabled, "downloads_disabled"}, {qdriver.ErrDownloadCanceled, "download_canceled"},
		{qdriver.ErrNetworkTrackingChanged, "network_tracking_changed"},
		{qdriver.ErrFrameNotFound, "frame_not_found"}, {qdriver.ErrFrameLimit, "frame_limit"},
		{qdriver.ErrUnsupported, "unsupported"}, {qdriver.ErrFillRejected, "fill_rejected"},
		{qdriver.ErrDocumentChanged, "document_changed"}, {qdriver.ErrBrowserClosed, "browser_closed"},
		{qdriver.ErrConnectionLost, "connection_lost"}, {qdriver.ErrPageUnavailable, "page_unavailable"},
		{os.ErrExist, "already_exists"}, {os.ErrNotExist, "not_found"},
		{context.Canceled, "canceled"},
	} {
		if errors.Is(err, test.err) {
			code = test.code
			break
		}
	}
	var own *commandError
	if errors.As(err, &own) {
		copy := *own
		copy.MayHaveEffects = copy.MayHaveEffects || effects
		return &copy
	}
	e := &commandError{Code: code, Message: err.Error(), MayHaveEffects: effects}
	var op *qdriver.OperationError
	if errors.As(err, &op) {
		e.Operation = op.Operation
		e.PageID = op.PageID
		e.MayHaveEffects = e.MayHaveEffects || op.MayHaveEffects
	}
	return e
}
func invalid(format string, args ...any) error {
	return &commandError{Code: "invalid_argument", Message: fmt.Sprintf(format, args...)}
}
func missing(kind, id string) error {
	return &commandError{Code: "not_found", Message: fmt.Sprintf("%s %q is not managed by this session", kind, id)}
}

// Discovery files contain a local capability, not SDK state or durable refs.
type discovery struct {
	Version        int           `json:"version"`
	Address        string        `json:"address"`
	Token          string        `json:"token"`
	Instance       string        `json:"instance"`
	PID            int           `json:"pid"`
	Session        string        `json:"session,omitempty"`
	EndpointHash   string        `json:"endpointHash,omitempty"`
	Chrome         string        `json:"chrome,omitempty"`
	Headless       bool          `json:"headless,omitempty"`
	InputMode      string        `json:"inputMode,omitempty"`
	CommandTimeout time.Duration `json:"commandTimeout,omitempty"`
	CleanupTimeout time.Duration `json:"cleanupTimeout,omitempty"`
}

func loadDiscovery(path string) (discovery, error) {
	var d discovery
	file, err := os.Open(path)
	if err != nil {
		return d, fmt.Errorf("read session file (run start first): %w", err)
	}
	defer func() { _ = file.Close() }()
	dec := json.NewDecoder(file)
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("session file is not ready or is invalid: %w", err)
	}
	host, port, err := net.SplitHostPort(d.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" || d.Version != protocolVersion || len(d.Token) != 64 || d.Instance == "" {
		return discovery{}, errors.New("invalid local session discovery file")
	}
	return d, nil
}

func readOnly(command string) bool {
	switch command {
	case "status", "task.list", "page.list", "page.title", "page.url", "page.frames", "page.snapshot", "page.wait-selector", "page.wait-idle", "events.next", "download.wait", "download.info", "dialog.current":
		return true
	default:
		return false
	}
}
