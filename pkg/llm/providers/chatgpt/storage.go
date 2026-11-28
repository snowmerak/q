package chatgpt

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type account struct {
	ClientID        string `json:"client_id,omitempty"`
	Subject         string `json:"subject,omitempty"`
	Email           string `json:"email,omitempty"`
	Access          string `json:"access_token,omitempty"`
	Refresh         string `json:"refresh_token,omitempty"`
	Scopes          string `json:"scopes,omitempty"`
	Expires         int64  `json:"expires_at,omitempty"`
	EarliestRefresh int64  `json:"earliest_refresh_at,omitempty"`
}

type state struct {
	Version      int                `json:"version"`
	AppID        string             `json:"app_id"`
	AppName      string             `json:"app_name"`
	HostID       string             `json:"ext_agent_host_id"`
	Active       string             `json:"active_profile,omitempty"`
	Last         string             `json:"last_profile,omitempty"`
	Accounts     map[string]account `json:"accounts"`
	Pending      string             `json:"pending_attempt,omitempty"`
	PendingUntil int64              `json:"pending_until,omitempty"`
	LoginError   string             `json:"login_error,omitempty"`
}

// withState serializes initialization, registration and rotating refresh tokens
// across processes, including overlapping Gateway generations. OS locks are
// released on process exit; a crashed process cannot leave a permanent lock.
func (p *Provider) withState(ctx context.Context, fn func(*state) error) error {
	if err := os.MkdirAll(p.options.Directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(p.options.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("chatgpt: credential directory must be a local directory")
	}
	if err := os.Chmod(p.options.Directory, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(p.options.Directory, "accounts.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	for {
		ok, err := tryLock(lock)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer unlock(lock)
	path := filepath.Join(p.options.Directory, "accounts.dat")
	var saved state
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80
		saved = state{Version: 1, AppID: p.options.AppID, AppName: p.options.AppName,
			HostID: fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), Accounts: make(map[string]account)}
		if err := p.save(&saved); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		plain, err := unprotect(data)
		if err != nil {
			return errors.New("chatgpt: credentials cannot be opened by this OS user")
		}
		if err := json.Unmarshal(plain, &saved); err != nil {
			return errors.New("chatgpt: invalid credential store; preserve it for recovery")
		}
	}
	if saved.Version != 1 || saved.AppID != p.options.AppID || saved.AppName != p.options.AppName || saved.HostID == "" || saved.Accounts == nil {
		return errors.New("chatgpt: credential store belongs to a different application or has an unsupported format")
	}
	return fn(&saved)
}

// save is called while holding the store lock. Sync and atomic replacement
// preserve the issued registration even if token exchange later fails.
func (p *Provider) save(saved *state) error {
	plain, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	data, err := protect(plain)
	if err != nil {
		return errors.New("chatgpt: OS credential protection failed")
	}
	file, err := os.CreateTemp(p.options.Directory, ".accounts-*")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return replaceFile(path, filepath.Join(p.options.Directory, "accounts.dat"))
}

func randomString() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}
