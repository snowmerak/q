package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(r *http.Request, code int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: r}
}

type authFixture struct {
	t                       *testing.T
	key                     *rsa.PrivateKey
	mu                      sync.Mutex
	nonce, subject, failure string
	challenge, redirect     string
	refreshes, revokes      int
	revocationStatus        int
}

func newFixture(t *testing.T) *authFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &authFixture{t: t, key: key, subject: "workspace-user"}
}
func (f *authFixture) client() *http.Client { return &http.Client{Transport: roundTrip(f.serve)} }
func (f *authFixture) serve(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.String() {
	case issuer + "/.well-known/openid-configuration":
		return reply(r, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/api/accounts/authorize", "token_endpoint": issuer + "/api/accounts/oauth/token", "jwks_uri": issuer + "/.well-known/jwks.json", "id_token_signing_alg_values_supported": []string{"RS256"}}), nil
	case issuer + "/.well-known/jwks.json":
		return reply(r, 200, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": "AQAB"}}}), nil
	case issuer + "/api/accounts/oauth/token":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if f.failure != "" {
			status := 400
			if f.failure == "temporarily_unavailable" {
				status = 503
			}
			return reply(r, status, map[string]string{"error": f.failure, "error_description": "SECRET must never appear"}), nil
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes++
			if r.Form.Get("refresh_token") != "initial-refresh" {
				f.t.Error("refresh token reused or not serialized")
			}
			return reply(r, 200, tokenSet{Access: "renewed-access", Refresh: "rotated-refresh", Type: "Bearer", Expires: 3600, Scope: planScope}), nil
		}
		if r.Form.Get("resource") != resource || r.Form.Get("redirect_uri") == "" || r.Form.Get("code_verifier") == "" {
			f.t.Error("missing exchange binding")
		}
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(challenge[:]) != f.challenge || r.Form.Get("redirect_uri") != f.redirect {
			f.t.Error("exchange changed PKCE or redirect binding")
		}
		enc := func(v any) string { data, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(data) }
		unsigned := enc(map[string]any{"alg": "RS256", "kid": "test"}) + "." + enc(map[string]any{"iss": issuer, "aud": r.Form.Get("client_id"), "sub": f.subject, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": f.nonce, "email": "example@example.test"})
		digest := sha256.Sum256([]byte(unsigned))
		signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
		if err != nil {
			return nil, err
		}
		return reply(r, 200, tokenSet{Access: "initial-access", Refresh: "initial-refresh", ID: unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), Type: "Bearer", Expires: 3600, Scope: planScope}), nil
	case issuer + "/api/accounts/oauth/revoke":
		f.revokes++
		if f.revocationStatus != 0 {
			return reply(r, f.revocationStatus, map[string]any{}), nil
		}
		return reply(r, 200, map[string]any{}), nil
	default:
		return nil, fmt.Errorf("unexpected network request %s", r.URL)
	}
}
func newTestProvider(t *testing.T, dir string, client *http.Client) *Provider {
	t.Helper()
	p, err := New(Options{Directory: dir, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func finishLogin(t *testing.T, p *Provider, f *authFixture, newAccount bool, tamper bool) url.Values {
	t.Helper()
	address, err := p.StartLogin(context.Background(), newAccount)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	q := u.Query()
	f.mu.Lock()
	f.nonce = q.Get("nonce")
	f.challenge, f.redirect = q.Get("code_challenge"), q.Get("redirect_uri")
	if tamper {
		f.nonce = "wrong-nonce"
	}
	f.mu.Unlock()
	callback, _ := url.Parse(q.Get("redirect_uri"))
	callback.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"test-code"}, "client_id": {"issued-client"}}.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("callback HTTP %d", response.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := p.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !status.Pending {
			return q
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("callback did not complete")
	return nil
}
func TestRegistrationRetainedAcrossFailureAndDisconnect(t *testing.T) {
	f := newFixture(t)
	f.failure = "invalid_grant"
	dir := t.TempDir()
	p := newTestProvider(t, dir, f.client())
	first := finishLogin(t, p, f, false, false)
	status, _ := p.Status(context.Background())
	if status.Error == "" || status.Active != "" {
		t.Fatal("failed login became active")
	}
	f.mu.Lock()
	f.failure = ""
	f.mu.Unlock()
	second := finishLogin(t, p, f, false, false)
	if first.Get("client_id") != "dynamic_agent_client" || second.Get("client_id") != "issued-client" || first.Get("ext_agent_host_id") != second.Get("ext_agent_host_id") {
		t.Fatal("failed exchange lost registration or host identity")
	}
	if second.Get("state") == first.Get("state") || second.Get("nonce") == first.Get("nonce") || second.Get("code_challenge") == first.Get("code_challenge") {
		t.Fatal("attempt secrets reused")
	}
	status, err := p.Status(context.Background())
	if err != nil || status.Active == "" || !status.Profiles[0].PlanEnabled || !status.Profiles[0].Verified {
		t.Fatalf("unverified account: %+v %v", status, err)
	}
	safe, _ := json.Marshal(status)
	for _, secret := range []string{"issued-client", "workspace-user", "initial-access", "initial-refresh", "urn:uuid"} {
		if strings.Contains(string(safe), secret) {
			t.Fatal("status exposed credential or internal identity")
		}
	}
	if runtime.GOOS == "windows" {
		disk, _ := os.ReadFile(filepath.Join(dir, "accounts.dat"))
		if strings.Contains(string(disk), "initial-refresh") || !strings.HasPrefix(string(disk), "SIWC-DPAPI-1\n") {
			t.Fatal("Windows credentials are not protected")
		}
	}
	if err := p.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	third := finishLogin(t, p, f, false, false)
	if third.Get("client_id") != "issued-client" || third.Get("ext_agent_host_id") != first.Get("ext_agent_host_id") {
		t.Fatal("disconnect changed registration")
	}
}
func TestInvalidIdentityRevokesMintedSession(t *testing.T) {
	f := newFixture(t)
	p := newTestProvider(t, t.TempDir(), f.client())
	finishLogin(t, p, f, false, true)
	status, _ := p.Status(context.Background())
	f.mu.Lock()
	revoked := f.revokes
	f.mu.Unlock()
	if status.Active != "" || status.Error == "" || revoked != 1 {
		t.Fatalf("invalid identity was accepted or session not revoked: %+v, revokes=%d", status, revoked)
	}
}
func seedAccount(t *testing.T, p *Provider) {
	t.Helper()
	if err := p.withState(context.Background(), func(s *state) error {
		s.Active = "account"
		s.Accounts[s.Active] = account{ClientID: "issued-client", Subject: "workspace-user", Access: "expired", Refresh: "initial-refresh", Scopes: planScope, Expires: 1}
		return p.save(s)
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRefreshSerializedAcrossOwners(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	a := newTestProvider(t, dir, f.client())
	b := newTestProvider(t, dir, f.client())
	seedAccount(t, a)
	var wg sync.WaitGroup
	for _, p := range []*Provider{a, b, a, b} {
		wg.Go(func() {
			token, err := p.accessToken(context.Background())
			if err != nil || token != "renewed-access" {
				t.Errorf("token=%q error=%v", token, err)
			}
		})
	}
	wg.Wait()
	f.mu.Lock()
	calls := f.refreshes
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("refreshes=%d, want one rotation", calls)
	}
}
func TestRefreshErrorsPreserveRegistration(t *testing.T) {
	for _, code := range []string{"invalid_grant", "invalid_client", "refresh_token_reused", "temporarily_unavailable"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t)
			f.failure = code
			p := newTestProvider(t, t.TempDir(), f.client())
			seedAccount(t, p)
			_, err := p.accessToken(context.Background())
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("missing or unsanitized failure")
			}
			_ = p.withState(context.Background(), func(s *state) error {
				a := s.Accounts[s.Active]
				if a.ClientID != "issued-client" || a.Subject != "workspace-user" {
					t.Error("registration lost")
				}
				retained := a.Refresh != ""
				if retained != (code == "temporarily_unavailable") {
					t.Errorf("unexpected token retention for %s", code)
				}
				return nil
			})
		})
	}
}

func TestFailedIdentityCleanupReportsUnconfirmedRevocation(t *testing.T) {
	f := newFixture(t)
	f.revocationStatus = 503
	p := newTestProvider(t, t.TempDir(), f.client())
	finishLogin(t, p, f, false, true)
	status, err := p.Status(context.Background())
	if err != nil || status.Active != "" || !strings.Contains(status.Error, "revocation was not confirmed") {
		t.Fatalf("cleanup failure was hidden: %+v %v", status, err)
	}
}
func TestInstallationBindingAndPendingGuard(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	a := newTestProvider(t, dir, f.client())
	b := newTestProvider(t, dir, f.client())
	if _, err := a.StartLogin(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartLogin(context.Background(), true); err == nil {
		t.Fatal("overlapping registration accepted")
	}
	if _, err := New(Options{Directory: dir, AppID: "q", AppName: "Q"}); err == nil {
		t.Fatal("cross-application store sharing accepted")
	}
}

func TestWrongCallbackStateCannotConsumeSignIn(t *testing.T) {
	f := newFixture(t)
	p := newTestProvider(t, t.TempDir(), f.client())
	address, err := p.StartLogin(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	callback, _ := url.Parse(u.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"state": {"wrong"}, "code": {"test-code"}, "client_id": {"issued-client"}}.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	status, err := p.Status(context.Background())
	if response.StatusCode != 400 || err != nil || !status.Pending || status.Active != "" {
		t.Fatalf("unexpected callback state: HTTP %d %+v %v", response.StatusCode, status, err)
	}
}

func TestRetiringOwnerWaitsForLoginAndCloseCancelsIt(t *testing.T) {
	f := newFixture(t)
	p := newTestProvider(t, t.TempDir(), f.client())
	if _, err := p.StartLogin(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.WaitForLogin(ctx); err != context.DeadlineExceeded {
		t.Fatalf("pending sign-in was not retained: %v", err)
	}
	status, _ := p.Status(context.Background())
	if !status.Pending {
		t.Fatal("wait deadline cancelled the callback")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEarliestRefreshIsRespected(t *testing.T) {
	f := newFixture(t)
	p := newTestProvider(t, t.TempDir(), f.client())
	seedAccount(t, p)
	err := p.withState(context.Background(), func(s *state) error {
		a := s.Accounts[s.Active]
		a.Expires = time.Now().Add(30 * time.Second).Unix()
		a.EarliestRefresh = time.Now().Add(time.Hour).Unix()
		s.Accounts[s.Active] = a
		return p.save(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := p.accessToken(context.Background())
	if err != nil || token != "expired" {
		t.Fatalf("valid token not used before eligibility: %q %v", token, err)
	}
	_ = p.withState(context.Background(), func(s *state) error {
		a := s.Accounts[s.Active]
		a.Expires = 1
		s.Accounts[s.Active] = a
		return p.save(s)
	})
	if _, err := p.accessToken(context.Background()); err == nil {
		t.Fatal("ineligible expired token renewed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refreshes != 0 {
		t.Fatal("refresh attempted before eligibility")
	}
}
