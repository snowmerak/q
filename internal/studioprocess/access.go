package studioprocess

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type invitation struct {
	path  string
	until time.Time
}

// Access protects managed engines; the original CLI HTTP surface remains
// unchanged. Control requests always require the private bearer credential.
func Access(endpoint Endpoint, next http.Handler) http.Handler {
	var mu sync.Mutex
	invitations := make(map[string]invitation)
	browserToken := Secret()
	u, _ := url.Parse(endpoint.URL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint.Secure || strings.HasPrefix(r.URL.Path, ControlPath) {
			w.Header().Set("Cache-Control", "no-store")
		}
		bearer := equal(r.Header.Get("Authorization"), "Bearer "+endpoint.Token)
		if strings.HasPrefix(r.URL.Path, ControlPath+"enter/") && r.Method == http.MethodGet {
			code := strings.TrimPrefix(r.URL.Path, ControlPath+"enter/")
			mu.Lock()
			invite, ok := invitations[code]
			delete(invitations, code)
			mu.Unlock()
			if !ok || !time.Now().Before(invite.until) || r.Host != u.Host {
				http.Error(w, "Browser link expired; open Studio again", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "q_studio_session", Value: browserToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, invite.path, http.StatusSeeOther)
			return
		}
		if strings.HasPrefix(r.URL.Path, ControlPath) {
			if !bearer {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == ControlPath+"status":
				_ = json.NewEncoder(w).Encode(map[string]any{"protocol": Protocol, "instance": endpoint.Instance})
			case r.Method == http.MethodPost && r.URL.Path == ControlPath+"launch":
				path := r.URL.Query().Get("path")
				parsed, err := url.Parse(path)
				if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") || parsed.IsAbs() || parsed.Host != "" {
					http.Error(w, "Invalid local path", http.StatusBadRequest)
					return
				}
				mu.Lock()
				for code, invite := range invitations {
					if time.Now().After(invite.until) {
						delete(invitations, code)
					}
				}
				if len(invitations) >= 64 {
					mu.Unlock()
					http.Error(w, "Too many pending browser links", http.StatusTooManyRequests)
					return
				}
				code := Secret()
				invitations[code] = invitation{path: path, until: time.Now().Add(time.Minute)}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]string{"path": ControlPath + "enter/" + code})
			default:
				http.NotFound(w, r)
			}
			return
		}
		if endpoint.Secure && !bearer {
			cookie, err := r.Cookie("q_studio_session")
			if err != nil || !equal(cookie.Value, browserToken) {
				http.Error(w, "Open Q Studio or run q studio to connect", http.StatusUnauthorized)
				return
			}
			origin := r.Header.Get("Origin")
			if r.Host != u.Host || r.Header.Get("Sec-Fetch-Site") == "cross-site" || (origin != "" && origin != endpoint.URL) || (r.Method != http.MethodGet && r.Method != http.MethodHead && origin != endpoint.URL) {
				http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
