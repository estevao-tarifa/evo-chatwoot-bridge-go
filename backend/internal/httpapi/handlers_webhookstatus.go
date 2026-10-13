package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

type webhookStatusSide struct {
	Configured bool   `json:"configured"`
	URLMasked  string `json:"urlMasked"`
	Error      string `json:"error,omitempty"`
}

type webhookStatusResponse struct {
	Evolution      webhookStatusSide `json:"evolution"`
	Chatwoot       webhookStatusSide `json:"chatwoot"`
	InstanceStatus string            `json:"instanceStatus"`
}

// connectionWebhookStatus reports whether each upstream webhook points at the
// bridge. URL fields are always masked (spec §1.5, §1.3): the token never
// appears here. configured is derived from the persisted flag and, when the
// upstream is reachable, from the observed URL prefix.
func (s *server) connectionWebhookStatus(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	conn, err := s.admin.Connections.Get(r.Context(), id)
	if errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	}
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}

	resp := webhookStatusResponse{
		Evolution: webhookStatusSide{
			Configured: conn.WebhookConfigured,
			URLMasked:  maskedWebhookURL(s.admin.BridgeURL, "evolution", conn.ID),
		},
		Chatwoot: webhookStatusSide{
			URLMasked: maskedWebhookURL(s.admin.BridgeURL, "chatwoot", conn.ID),
		},
	}

	if s.admin.Upstream != nil {
		if evo, cw, err := s.admin.Upstream.ForConnection(r.Context(), id); err == nil {
			resp.InstanceStatus = instanceStatus(r, evo)
			if cw != nil && conn.ChatwootInboxID > 0 {
				inbox, err := cw.GetInbox(r.Context(), conn.ChatwootInboxID)
				switch {
				case err != nil:
					resp.Chatwoot.Error = "chatwoot unreachable"
				default:
					resp.Chatwoot.Configured = strings.Contains(inbox.WebhookURL, chatwootWebhookPrefix(conn.ID))
				}
			}
		}
	}
	writeJSON(s.log(r), w, http.StatusOK, resp)
}

// chatwootWebhookPrefix is the token-free path prefix a configured inbox
// webhook URL must contain (spec §1.3: match the new URL pattern without the
// token).
func chatwootWebhookPrefix(connectionID int) string {
	return "/api/webhooks/chatwoot/" + strconv.Itoa(connectionID) + "/"
}

// maskedWebhookURL renders the webhook URL with its token segment masked. It is
// never the full URL.
func maskedWebhookURL(base, system string, connectionID int) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/api/webhooks/" + system + "/" + strconv.Itoa(connectionID) + "/***"
}

// maskURLToken masks the last path segment of raw, where the bridge webhook
// token lives, plus any query-string values and the whole fragment, so a URL
// echoed back from an upstream inbox never discloses a credential. The scheme,
// host, preceding path and query parameter names survive so the URL stays
// recognisable; a URL with nothing to mask is returned unchanged. The full URL
// is revealed only by the two /webhook-url endpoints (spec §6.5 R2).
func maskURLToken(raw string) string {
	if raw == "" {
		return ""
	}
	path, suffix := raw, ""
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		path, suffix = raw[:i], raw[i:]
	}
	// Never touch the "scheme://" separator.
	searchFrom := 0
	if i := strings.Index(path, "://"); i >= 0 {
		searchFrom = i + 3
	}
	idx := strings.LastIndex(path, "/")
	if idx < searchFrom || idx == len(path)-1 {
		return path + maskQueryFragment(suffix) // no path segment; still mask the tail
	}
	return path[:idx+1] + "***" + maskQueryFragment(suffix)
}

// maskQueryFragment hides a query string's values (keeping parameter names) and
// masks a fragment entirely, so a credential carried outside the path segment
// cannot survive. suffix is "" or begins with "?" or "#".
func maskQueryFragment(suffix string) string {
	if suffix == "" {
		return ""
	}
	if suffix[0] == '#' {
		return "#***"
	}
	parts := strings.Split(suffix[1:], "&")
	for i, part := range parts {
		switch eq := strings.IndexByte(part, '='); {
		case eq >= 0:
			parts[i] = part[:eq+1] + "***"
		case part != "":
			parts[i] = "***"
		}
	}
	return "?" + strings.Join(parts, "&")
}

// instanceStatus best-effort reads a connection state from the untyped
// Evolution status object; the exact shape is [needs live validation].
func instanceStatus(r *http.Request, evo evolutionStatusReader) string {
	status, err := evo.GetInstanceStatus(r.Context())
	if err != nil {
		return "unknown"
	}
	for _, key := range []string{"state", "status", "connectionStatus"} {
		if v, ok := status[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	if instance, ok := status["instance"].(map[string]any); ok {
		if s, ok := instance["state"].(string); ok && s != "" {
			return s
		}
	}
	return "unknown"
}

// evolutionStatusReader is the narrow port instanceStatus needs.
type evolutionStatusReader interface {
	GetInstanceStatus(ctx context.Context) (map[string]any, error)
}
