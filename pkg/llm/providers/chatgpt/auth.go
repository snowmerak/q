// Package chatgpt uses Sign in with ChatGPT to call the public Responses API.
// It owns application registration and local credentials; it never reads Codex
// credentials or uses ChatGPT's private backend API.
package chatgpt

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const issuer = "https://auth.openai.com"
const resource = "https://api.openai.com/v1"
const planScope = "chatgpt.tokens.use.direct"

// Options identifies the owning application. Keep these values and Directory
// stable across restarts. A library host should pass its own application name.
type Options struct {
	Directory  string
	AppID      string
	AppName    string
	HTTPClient *http.Client
}

type Provider struct {
	options Options
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	loginWG sync.WaitGroup
}

func New(options Options) (*Provider, error) {
	if options.AppID == "" {
		options.AppID = "llm-provider"
	}
	if options.AppName == "" {
		options.AppName = "llm-provider"
	}
	if options.Directory == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		// Roaming AppData can copy installation identity to another Windows host.
		if runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "" {
			root = os.Getenv("LOCALAPPDATA")
		}
		options.Directory = filepath.Join(root, "llm-provider", "chatgpt")
	}
	if strings.ContainsAny(options.AppID, "\r\n\t ") {
		return nil, errors.New("chatgpt: app ID cannot contain whitespace")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	// Credentials must never follow a redirect to another serving endpoint.
	copyClient := *options.HTTPClient
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	options.HTTPClient = &copyClient
	ctx, cancel := context.WithCancel(context.Background())
	p := &Provider{options: options, ctx: ctx, cancel: cancel}
	if err := p.withState(ctx, func(_ *state) error { return nil }); err != nil {
		cancel()
		return nil, err
	}
	return p, nil
}

// Profile and Status are safe for settings UIs. They contain no OAuth tokens,
// account subject, client ID or installation host ID.
type Profile struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Connected   bool   `json:"connected"`
	Verified    bool   `json:"verified"`
	PlanEnabled bool   `json:"plan_enabled"`
}
type Status struct {
	Active   string    `json:"active_profile"`
	Profiles []Profile `json:"profiles"`
	Pending  bool      `json:"pending"`
	Error    string    `json:"error,omitempty"`
}

func (p *Provider) Status(ctx context.Context) (Status, error) {
	result := Status{Profiles: make([]Profile, 0)}
	err := p.withState(ctx, func(saved *state) error {
		result.Active, result.Pending, result.Error = saved.Active, saved.PendingUntil > time.Now().Unix(), saved.LoginError
		for id, value := range saved.Accounts {
			label := value.Email
			if label == "" {
				label = "ChatGPT account"
			}
			suffix := id
			if len(suffix) > 6 {
				suffix = suffix[len(suffix)-6:]
			}
			label += " · " + suffix
			result.Profiles = append(result.Profiles, Profile{ID: id, Label: label, Verified: value.Subject != "", Connected: value.Subject != "" && (value.Refresh != "" || value.Access != "" && value.Expires > time.Now().Unix()), PlanEnabled: hasScope(value.Scopes, planScope)})
		}
		slices.SortFunc(result.Profiles, func(a, b Profile) int { return strings.Compare(a.ID, b.ID) })
		return nil
	})
	return result, err
}

func (p *Provider) Select(ctx context.Context, profile string) error {
	return p.withState(ctx, func(saved *state) error {
		if saved.PendingUntil > time.Now().Unix() {
			return errors.New("chatgpt: finish or cancel the pending sign-in first")
		}
		if value, ok := saved.Accounts[profile]; !ok || value.Subject == "" {
			return errors.New("chatgpt: account has not been verified")
		}
		saved.Active = profile
		return p.save(saved)
	})
}

type tokenSet struct {
	Access          string `json:"access_token"`
	Refresh         string `json:"refresh_token"`
	ID              string `json:"id_token"`
	Type            string `json:"token_type"`
	Scope           string `json:"scope"`
	Expires         int64  `json:"expires_in"`
	EarliestRefresh int64  `json:"earliest_refresh_at"`
}

type oauthError struct {
	Code   string
	Status int
}

func (e *oauthError) Error() string {
	return fmt.Sprintf("chatgpt: OAuth %s (HTTP %d)", e.Code, e.Status)
}

func (p *Provider) exchange(ctx context.Context, form url.Values) (tokenSet, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenSet{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.options.HTTPClient.Do(request)
	if err != nil {
		return tokenSet{}, errors.New("chatgpt: OAuth token request failed; registration preserved")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body)
		if !slices.Contains([]string{"invalid_grant", "invalid_client", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused", "refresh_token_invalid"}, body.Error) {
			body.Error = "request_failed"
		}
		return tokenSet{}, &oauthError{Code: body.Error, Status: response.StatusCode}
	}
	var value tokenSet
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return value, errors.New("chatgpt: invalid OAuth token response")
	}
	if value.Access == "" || !strings.EqualFold(value.Type, "Bearer") || value.Expires <= 0 {
		return value, errors.New("chatgpt: missing OAuth bearer credential or expiry")
	}
	return value, nil
}

func hasScope(granted, scope string) bool { return slices.Contains(strings.Fields(granted), scope) }

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	var result string
	err := p.withState(ctx, func(saved *state) error {
		value, ok := saved.Accounts[saved.Active]
		if !ok || value.Subject == "" || !hasScope(value.Scopes, planScope) {
			return errors.New("chatgpt: sign in and authorize ChatGPT plan usage first")
		}
		if value.Access != "" && value.Expires > time.Now().Unix()+60 {
			result = value.Access
			return nil
		}
		if value.EarliestRefresh > time.Now().Unix() && value.Expires > time.Now().Unix() {
			result = value.Access
			return nil
		}
		if value.Refresh == "" {
			return errors.New("chatgpt: sign in again; no renewable session is available")
		}
		if value.EarliestRefresh > time.Now().Unix() {
			return errors.New("chatgpt: token renewal is not yet permitted")
		}
		renewed, err := p.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {value.ClientID}, "refresh_token": {value.Refresh}, "resource": {resource}})
		if err != nil {
			var failure *oauthError
			if errors.As(err, &failure) && slices.Contains([]string{"invalid_grant", "invalid_client", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused", "refresh_token_invalid"}, failure.Code) {
				value.Access, value.Refresh, value.Scopes = "", "", ""
				saved.Accounts[saved.Active] = value
				if saveErr := p.save(saved); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
		if renewed.Refresh == "" {
			value.Access, value.Refresh, value.Scopes = "", "", ""
			saved.Accounts[saved.Active] = value
			if err := p.save(saved); err != nil {
				return err
			}
			return errors.New("chatgpt: refresh returned no rotating refresh token")
		}
		value.Access, value.Refresh, value.Expires, value.EarliestRefresh = renewed.Access, renewed.Refresh, time.Now().Unix()+renewed.Expires, renewed.EarliestRefresh
		if renewed.Scope != "" {
			value.Scopes = renewed.Scope
		}
		saved.Accounts[saved.Active] = value
		if err := p.save(saved); err != nil {
			return err
		}
		if !hasScope(value.Scopes, planScope) {
			return errors.New("chatgpt: ChatGPT plan permission is no longer granted")
		}
		result = value.Access
		return nil
	})
	return result, err
}

// StartLogin returns a browser URL, never a token. newAccount must represent an
// explicit Add account action; ordinary reauthorization reuses the issued ID.
func (p *Provider) StartLogin(ctx context.Context, newAccount bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("chatgpt: provider is closed")
	}
	discoveryCtx, cancelDiscovery := context.WithTimeout(oidc.ClientContext(ctx, p.options.HTTPClient), 20*time.Second)
	defer cancelDiscovery()
	identityProvider, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return "", errors.New("chatgpt: OpenID discovery failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	stateValue, nonce, verifier, attempt := randomString(), randomString(), randomString(), randomString()
	var profile, clientID, hostID string
	err = p.withState(ctx, func(saved *state) error {
		if saved.PendingUntil > time.Now().Unix() {
			return errors.New("chatgpt: a sign-in is already pending for this installation")
		}
		profile = saved.Active
		if profile == "" {
			profile = saved.Last
		}
		if newAccount || profile == "" {
			profile = "account_" + randomString()[:16]
			saved.Accounts[profile] = account{}
		}
		clientID, hostID = saved.Accounts[profile].ClientID, saved.HostID
		saved.Last, saved.Pending, saved.PendingUntil, saved.LoginError = profile, attempt, time.Now().Add(10*time.Minute).Unix(), ""
		return p.save(saved)
	})
	if err != nil {
		_ = listener.Close()
		return "", err
	}
	query := url.Values{"client_id": {clientID}, "ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "resource": {resource}, "state": {stateValue}, "nonce": {nonce}, "code_challenge_method": {"S256"}}
	if clientID == "" {
		query.Set("client_id", "dynamic_agent_client")
		query.Set("agent_name_hint", p.options.AppName)
	}
	digest := sha256.Sum256([]byte(verifier))
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(digest[:]))
	type callback struct{ code, client, errorCode string }
	callbacks := make(chan callback, 1)
	var callbackMu sync.Mutex
	consumed := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(stateValue)) != 1 {
			http.Error(w, "Invalid sign-in callback", 400)
			return
		}
		issued := q.Get("client_id")
		if clientID != "" {
			if issued != "" && issued != clientID {
				http.Error(w, "Client registration changed", 400)
				return
			}
			issued = clientID
		}
		if q.Get("error") == "" && (q.Get("code") == "" || issued == "" || issued == "dynamic_agent_client") {
			http.Error(w, "Incomplete sign-in callback", 400)
			return
		}
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if consumed {
			http.Error(w, "Callback already consumed", http.StatusConflict)
			return
		}
		consumed = true
		failure := ""
		if q.Get("error") != "" {
			failure = "consent_declined"
		}
		callbacks <- callback{code: q.Get("code"), client: issued, errorCode: failure}
		_, _ = io.WriteString(w, "Sign-in callback received. Return to your application to check the connection.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	loginCtx, cancelLogin := context.WithTimeout(p.ctx, 10*time.Minute)
	p.loginWG.Add(1)
	go func() {
		defer p.loginWG.Done()
		defer cancelLogin()
		defer func() { _ = server.Close() }()
		go func() { _ = server.Serve(listener) }()
		failure := errors.New("chatgpt: sign-in cancelled or timed out; retry the saved registration")
		select {
		case returned := <-callbacks:
			if returned.errorCode != "" {
				failure = errors.New("chatgpt: consent declined")
			} else {
				failure = p.completeLogin(oidc.ClientContext(loginCtx, p.options.HTTPClient), identityProvider, profile, attempt, returned.client, returned.code, verifier, redirect, nonce)
			}
		case <-loginCtx.Done():
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.withState(cleanup, func(saved *state) error {
			if saved.Pending != attempt {
				return nil
			}
			saved.Pending, saved.PendingUntil = "", 0
			if failure != nil {
				saved.LoginError = failure.Error()
			}
			return p.save(saved)
		})
	}()
	return issuer + "/api/accounts/authorize?" + query.Encode(), nil
}

func (p *Provider) completeLogin(ctx context.Context, identityProvider *oidc.Provider, profile, attempt, clientID, code, verifier, redirect, nonce string) (returnErr error) {
	err := p.withState(ctx, func(saved *state) error {
		if saved.Pending != attempt {
			return errors.New("chatgpt: sign-in attempt is no longer active")
		}
		value := saved.Accounts[profile]
		// Persist the issued ID BEFORE exchange or validation. A failure retries
		// this registration without initiating another dynamic registration.
		value.ClientID = clientID
		saved.Accounts[profile] = value
		return p.save(saved)
	})
	if err != nil {
		return err
	}
	// Keep status polling responsive while exchange and JWKS validation run.
	tokens, err := p.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {resource}})
	stored := false
	defer func() {
		if !stored && tokens.Refresh != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := p.revoke(cleanup, clientID, tokens.Refresh); err != nil {
				returnErr = errors.Join(returnErr, errors.New("chatgpt: session revocation was not confirmed; disconnect this app in ChatGPT settings"))
			}
		}
	}()
	if err != nil {
		return err
	}
	identity, err := identityProvider.Verifier(&oidc.Config{ClientID: clientID}).Verify(ctx, tokens.ID)
	if err != nil {
		return errors.New("chatgpt: ID token validation failed; registration preserved")
	}
	var claims struct {
		Nonce string `json:"nonce"`
		Email string `json:"email"`
	}
	if err := identity.Claims(&claims); err != nil || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 || identity.Subject == "" {
		return errors.New("chatgpt: ID token nonce or subject validation failed")
	}
	err = p.withState(ctx, func(saved *state) error {
		if saved.Pending != attempt || ctx.Err() != nil {
			return errors.New("chatgpt: sign-in attempt is no longer active")
		}
		value := saved.Accounts[profile]
		if value.ClientID != clientID {
			return errors.New("chatgpt: registration changed during sign-in")
		}
		if value.Subject != "" && value.Subject != identity.Subject {
			return errors.New("chatgpt: reauthorization returned a different account")
		}
		value.Subject, value.Email, value.Access, value.Refresh, value.Scopes, value.Expires, value.EarliestRefresh = identity.Subject, claims.Email, tokens.Access, tokens.Refresh, tokens.Scope, time.Now().Unix()+tokens.Expires, tokens.EarliestRefresh
		saved.Accounts[profile] = value
		saved.Active = profile
		return p.save(saved)
	})
	stored = err == nil
	return err
}

func (p *Provider) revoke(ctx context.Context, clientID, token string) error {
	form := url.Values{"token": {token}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.options.HTTPClient.Do(request)
	if err != nil {
		return errors.New("chatgpt: remote revocation failed; credentials retained for retry")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return fmt.Errorf("chatgpt: revocation HTTP %d; credentials retained for retry", response.StatusCode)
	}
	return nil
}

// Disconnect revokes only the selected account's renewable session and retains
// its registration and this installation's host ID for reauthorization.
func (p *Provider) Disconnect(ctx context.Context) error {
	return p.withState(ctx, func(saved *state) error {
		if saved.PendingUntil > time.Now().Unix() {
			return errors.New("chatgpt: finish the pending sign-in first")
		}
		value, ok := saved.Accounts[saved.Active]
		if !ok {
			return nil
		}
		if value.Refresh != "" {
			if err := p.revoke(ctx, value.ClientID, value.Refresh); err != nil {
				return err
			}
		}
		value.Access, value.Refresh, value.Scopes = "", "", ""
		value.Expires = 0
		saved.Accounts[saved.Active] = value
		return p.save(saved)
	})
}

func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	p.loginWG.Wait()
	return nil
}

// WaitForLogin lets a retiring Gateway keep its loopback callback alive. Stop
// admitting new sign-in requests before calling this; Close still cancels login.
func (p *Provider) WaitForLogin(ctx context.Context) error {
	done := make(chan struct{})
	go func() { p.loginWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
