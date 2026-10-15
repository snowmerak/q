package websearch

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/diagnostics"
)

func logID(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))[:16] }

// Operational URL logs omit credentials, fragments, session path parameters and
// arbitrary query values. Numeric document selectors remain useful for diagnosis.
func logURL(value string) string {
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || !validURL(value) {
		return "invalid"
	}
	u.User = nil
	u.Fragment = ""
	parts := strings.Split(u.Path, "/")
	for i, part := range parts {
		parts[i], _, _ = strings.Cut(part, ";")
	}
	u.Path = strings.Join(parts, "/")
	u.RawPath = ""
	filtered := url.Values{}
	for _, key := range []string{"boardID", "boardSeq", "boardId", "boardMasterId", "newsId", "menuId", "page", "m"} {
		v := u.Query().Get(key)
		if len(v) > 0 && len(v) <= 20 && strings.Trim(v, "0123456789") == "" {
			filtered.Set(key, v)
		}
	}
	u.RawQuery = filtered.Encode()
	return clip(u.String(), 1600)
}

func searchLogContext(ctx context.Context) context.Context {
	return diagnostics.WithAttrs(ctx, slog.String("search_id", uuid.NewString()))
}

func searchStarted(ctx context.Context, in Input) func(Output, error) {
	started := time.Now()
	query := in.Query
	if query == "" {
		query = clip(in.Goal, 1000)
	}
	budget := in.MaxSteps
	if budget == 0 {
		budget = 20
	}
	diagnostics.Logger(ctx).Info("web_search.started", "query_id", logID(query), "query_bytes", len(query), "page_budget", budget, "requirement_count", len(in.Requirements))
	return func(out Output, err error) {
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
		}
		diagnostics.Logger(ctx).Log(ctx, level, "web_search.completed", "status", out.Status, "stop_reason", out.StopReason, "page_count", out.Steps, "evidence_count", len(out.Evidence), "unresolved_count", len(out.Unresolved), "model_calls", out.SystemOneCalls, "duration_ms", time.Since(started).Milliseconds(), "reason_code", failureCode(err))
	}
}

func failureCode(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var certificate x509.HostnameError
	if errors.As(err, &certificate) {
		return "tls_hostname"
	}
	var api *systemone.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("model_http_%d", api.StatusCode)
	}
	text := err.Error()
	for _, match := range [][2]string{{"ERR_NAME_NOT_RESOLVED", "dns_error"}, {"ERR_TOO_MANY_REDIRECTS", "redirect_limit"}, {"stopped after", "redirect_limit"}, {"ERR_CERT_", "tls_error"}, {"ERR_CONNECTION_TIMED_OUT", "timeout"}, {"ERR_CONNECTION", "connection_error"}, {"Too Many Requests", "rate_limited"}, {"Forbidden", "forbidden"}, {"no results parsed", "parse_empty"}, {"token not found", "challenge"}, {"HTTP ", "http_error"}} {
		if strings.Contains(text, match[0]) {
			return match[1]
		}
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	return "operation_error"
}
