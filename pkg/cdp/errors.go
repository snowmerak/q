package qdriver

import (
	"errors"
	"fmt"
)

// Lifecycle errors can be inspected with errors.Is.
var (
	ErrBrowserClosed          = errors.New("browser connection is closed")
	ErrConnectionLost         = errors.New("browser connection was lost")
	ErrTaskFinished           = errors.New("task is finished")
	ErrPageReleased           = errors.New("page is no longer managed")
	ErrPageNotFound           = errors.New("page does not exist")
	ErrPageInUse              = errors.New("page already belongs to a task")
	ErrPageUnavailable        = errors.New("page session is unavailable; close or release the handle")
	ErrNotOwned               = errors.New("cannot close a borrowed page; release it instead")
	ErrInvalidPage            = errors.New("page does not belong to this task")
	ErrStaleRef               = errors.New("ref is stale; take a new snapshot")
	ErrWrongPage              = errors.New("ref belongs to another page")
	ErrElementNotFound        = errors.New("element not found")
	ErrAmbiguous              = errors.New("locator matches multiple elements")
	ErrInvalidLocator         = errors.New("invalid CSS locator")
	ErrNotActionable          = errors.New("element is not visible, enabled, or the hit target")
	ErrNotEditable            = errors.New("element is not a supported editable control")
	ErrUnsupported            = errors.New("operation is not supported")
	ErrDocumentChanged        = errors.New("document changed during the operation")
	ErrFillRejected           = errors.New("field did not retain the requested text")
	ErrFrameNotFound          = errors.New("frame is no longer reachable")
	ErrFrameLimit             = errors.New("page frame capacity exceeded")
	ErrSubscriptionClosed     = errors.New("event subscription is closed")
	ErrEventOverflow          = errors.New("event capacity exceeded; events may have been lost")
	ErrDialogOpen             = errors.New("JavaScript dialog is open; handle it before issuing renderer commands")
	ErrDialogStale            = errors.New("dialog is no longer open")
	ErrDownloadsDisabled      = errors.New("configure a download directory first")
	ErrDownloadCanceled       = errors.New("download was canceled or failed")
	ErrNetworkTrackingChanged = errors.New("network tracking sessions changed; prepare a new idle wait")
)

// OperationError adds context to a failed operation without hiding its cause.
type OperationError struct {
	Operation string
	PageID    string
	// MayHaveEffects is conservative: a mutating command may have been sent.
	// Cancellation does not roll back those effects. Do not blindly retry it.
	MayHaveEffects bool
	Err            error
}

func (e *OperationError) Error() string {
	if e.PageID != "" {
		return fmt.Sprintf("qdriver %s (page %s): %v", e.Operation, e.PageID, e.Err)
	}
	return fmt.Sprintf("qdriver %s: %v", e.Operation, e.Err)
}

func (e *OperationError) Unwrap() error { return e.Err }
