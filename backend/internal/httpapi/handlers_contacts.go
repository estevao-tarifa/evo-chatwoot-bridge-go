package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// contactsSyncResponse is the contacts/sync result (spec §1.2).
type contactsSyncResponse struct {
	Total   int      `json:"total"`
	Synced  int      `json:"synced"`
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// maxContactSyncErrors bounds the returned error list (legacy: ≤5).
const maxContactSyncErrors = 5

// connectionContactsSync imports Evolution contacts into Chatwoot. It reuses an
// existing exact contact before creating one (identity integrity, AGENTS §7)
// and never creates a group contact from a non-authoritative name.
// [needs live validation]: the Evolution contact shape is not contracted.
func (s *server) connectionContactsSync(w http.ResponseWriter, r *http.Request) {
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
	if conn.ChatwootInboxID <= 0 {
		invalidRequest(w, s.log(r), "connection has no chatwoot inbox")
		return
	}
	if s.admin.Upstream == nil {
		internalError(w, s.log(r), errors.New("upstream factory not wired"))
		return
	}
	evo, cw, err := s.admin.Upstream.ForConnection(r.Context(), id)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	if evo == nil || cw == nil {
		upstreamError(w, s.log(r))
		return
	}

	contacts, err := evo.FetchContacts(r.Context())
	if err != nil {
		upstreamError(w, s.log(r))
		return
	}

	result := contactsSyncResponse{Total: len(contacts)}
	for _, raw := range contacts {
		identity, name := contactIdentity(raw)
		if identity == "" || isGroupLikeIdentity(identity) {
			result.Skipped++
			continue
		}
		existing, err := cw.SearchContactExact(r.Context(), identity, admin.ContactKindDirect)
		if err != nil {
			result.Failed++
			result.addError("contact lookup failed")
			continue
		}
		if existing != nil {
			result.Skipped++
			continue
		}
		if _, err := cw.CreateContact(r.Context(), admin.ContactParams{
			Kind:     admin.ContactKindDirect,
			Identity: identity,
			Name:     name,
		}); err != nil {
			result.Failed++
			result.addError("contact create failed")
			continue
		}
		result.Synced++
	}
	writeJSON(s.log(r), w, http.StatusOK, result)
}

func (r *contactsSyncResponse) addError(message string) {
	if len(r.Errors) < maxContactSyncErrors {
		r.Errors = append(r.Errors, message)
	}
}

// contactIdentity extracts the stable identity and display name from an
// untyped Evolution contact object. The key set is best-effort until the shape
// is contracted [needs live validation].
func contactIdentity(raw map[string]any) (identity, name string) {
	for _, key := range []string{"id", "remoteJid", "jid", "number", "phone"} {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			identity = strings.TrimSpace(v)
			break
		}
	}
	for _, key := range []string{"name", "pushName", "verifiedName", "notify"} {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			name = strings.TrimSpace(v)
			break
		}
	}
	return identity, name
}

// isGroupLikeIdentity reports whether a JID is a group/broadcast/newsletter,
// which the contacts sync must not import (legacy filter).
func isGroupLikeIdentity(identity string) bool {
	lower := strings.ToLower(identity)
	return strings.Contains(lower, "@g.us") ||
		strings.Contains(lower, "broadcast") ||
		strings.Contains(lower, "newsletter")
}
