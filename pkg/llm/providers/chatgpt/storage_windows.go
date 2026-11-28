package chatgpt

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tryLock(file *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
func unlock(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

// User-scoped DPAPI avoids plaintext credentials. Copied state is readable only
// where that user's DPAPI keys are available; it is not an export format.
func protect(data []byte) ([]byte, error) { return crypt(data, false) }
func unprotect(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte("SIWC-DPAPI-1\n")) {
		return nil, errors.New("unprotected credential file")
	}
	return crypt(data[len("SIWC-DPAPI-1\n"):], true)
}
func crypt(data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty credential data")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	result := bytes.Clone(unsafe.Slice(out.Data, int(out.Size)))
	if !decrypt {
		result = append([]byte("SIWC-DPAPI-1\n"), result...)
	}
	return result, nil
}
func replaceFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
