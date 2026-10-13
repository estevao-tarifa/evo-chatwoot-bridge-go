package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// webhookSubscribeEvents is the subscription list sent with the Evolution
// SetWebhook call (POST /instance/connect, internal/evolution/messages.go).
//
// The values are the exact uppercase enum Evolution GO 0.7.2 validates against,
// in the order the live production instance runs today; its server discards any
// value outside the enum (docs/contracts/evolution/webhooks.md §2.1,
// `0.7.2/pkg/instance/service/instance_service.go` + `pkg/internal/event_types`).
// [live-verified 2026-10-13] these 16 names are the subscription the working
// production instance carries.
//
// MESSAGES_UPSERT must never return here: it is an Evolution API v2 / Node
// event name, absent from the GO enum, so GO silently discards it and the
// instance would end up subscribed to nothing — WhatsApp events would stop
// reaching Chatwoot. The legacy PascalCase list (`Message`, `QrcodeUpdated`, …,
// evo-chatwoot-bridge/server/services/evolutionClient.ts:115-129) is discarded
// for the same reason and is not used here.
var webhookSubscribeEvents = []string{
	"MESSAGE",
	"SEND_MESSAGE",
	"READ_RECEIPT",
	"PRESENCE",
	"HISTORY_SYNC",
	"CHAT_PRESENCE",
	"CALL",
	"CONNECTION",
	"LABEL",
	"CONTACT",
	"GROUP",
	"NEWSLETTER",
	"QRCODE",
	"BUTTON_CLICK",
	"PICTURE",
	"USER_ABOUT",
}

type connectionCreateRequest struct {
	Name                   string  `json:"name"`
	EvolutionAPIURL        string  `json:"evolutionApiUrl"`
	EvolutionAPIKey        string  `json:"evolutionApiKey"`
	EvolutionInstanceName  string  `json:"evolutionInstanceName"`
	EvolutionInstanceToken string  `json:"evolutionInstanceToken"`
	ChatwootAPIURL         string  `json:"chatwootApiUrl"`
	ChatwootAPIToken       string  `json:"chatwootApiToken"`
	ChatwootAccountID      int     `json:"chatwootAccountId"`
	ChatwootInboxID        int     `json:"chatwootInboxId"`
	IgnoreGroups           *bool   `json:"ignoreGroups"`
	SignMessages           *bool   `json:"signMessages"`
	AutoReadMessages       *bool   `json:"autoReadMessages"`
	RejectCalls            *bool   `json:"rejectCalls"`
	RejectCallMessage      *string `json:"rejectCallMessage"`
	N8NWebhookURL          *string `json:"n8nWebhookUrl"`
}

type connectionListResponse struct {
	Connections []connectionDTO `json:"connections"`
}

type connectionTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type connectionTestResponse struct {
	Success   bool                 `json:"success"`
	Evolution connectionTestResult `json:"evolution"`
	Chatwoot  connectionTestResult `json:"chatwoot"`
}

type webhookURLResponse struct {
	WebhookURL string `json:"webhookUrl"`
}

func (s *server) connectionsList(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	rows, err := s.admin.Connections.List(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	out := make([]connectionDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, toConnectionDTO(c))
	}
	writeJSON(s.log(r), w, http.StatusOK, connectionListResponse{Connections: out})
}

func (s *server) connectionGet(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	conn, err := s.admin.Connections.Get(r.Context(), id)
	if errors.Is(err, admin.ErrNotFound) || (err == nil && conn == nil) {
		notFound(w, s.log(r), "connection not found")
		return
	}
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, toConnectionDTO(*conn))
}

func (s *server) connectionCreate(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var req connectionCreateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	in := admin.ConnectionCreate{
		Name:                   strings.TrimSpace(req.Name),
		EvolutionAPIURL:        strings.TrimSpace(req.EvolutionAPIURL),
		EvolutionAPIKey:        req.EvolutionAPIKey,
		EvolutionInstanceName:  strings.TrimSpace(req.EvolutionInstanceName),
		EvolutionInstanceToken: req.EvolutionInstanceToken,
		ChatwootAPIURL:         strings.TrimSpace(req.ChatwootAPIURL),
		ChatwootAPIToken:       req.ChatwootAPIToken,
		ChatwootAccountID:      req.ChatwootAccountID,
		ChatwootInboxID:        req.ChatwootInboxID,
		IgnoreGroups:           boolOr(req.IgnoreGroups, true),
		SignMessages:           boolOr(req.SignMessages, false),
		AutoReadMessages:       boolOr(req.AutoReadMessages, false),
		RejectCalls:            boolOr(req.RejectCalls, false),
	}
	if req.RejectCallMessage != nil {
		in.RejectCallMessage = *req.RejectCallMessage
	}
	if req.N8NWebhookURL != nil {
		in.N8NWebhookURL = *req.N8NWebhookURL
	}
	if err := validateConnectionCreate(in); err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	conn, err := s.admin.Connections.Create(r.Context(), in)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusCreated, toConnectionDTO(*conn))
}

func (s *server) connectionUpdate(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !s.decodeJSON(w, r, &raw) {
		return
	}
	values, err := connectionUpdateValues(raw)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	if len(values) == 0 {
		invalidRequest(w, s.log(r), "no updatable fields supplied")
		return
	}
	conn, err := s.admin.Connections.Update(r.Context(), id, values)
	if errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	}
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, toConnectionDTO(*conn))
}

func (s *server) connectionDelete(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	err := s.admin.Connections.Deactivate(r.Context(), id)
	if errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	}
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, successResponse{Success: true})
}

func (s *server) connectionTest(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.admin.Connections.Get(r.Context(), id); errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	} else if err != nil {
		internalError(w, s.log(r), err)
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

	var result connectionTestResponse
	if evo != nil && evo.HealthCheck(r.Context()) == nil {
		result.Evolution.OK = true
	} else {
		result.Evolution.Error = "evolution unreachable"
	}
	if cw != nil {
		if _, err := cw.ListInboxes(r.Context()); err == nil {
			result.Chatwoot.OK = true
		} else {
			result.Chatwoot.Error = "chatwoot unreachable"
		}
	} else {
		result.Chatwoot.Error = "chatwoot not configured"
	}
	result.Success = result.Evolution.OK && result.Chatwoot.OK
	writeJSON(s.log(r), w, http.StatusOK, result)
}

func (s *server) connectionResolveToken(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.admin.Connections.Get(r.Context(), id); errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	} else if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	if s.admin.Upstream == nil {
		internalError(w, s.log(r), errors.New("upstream factory not wired"))
		return
	}
	evo, _, err := s.admin.Upstream.ForConnection(r.Context(), id)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	token, err := evo.ResolveInstanceToken(r.Context())
	if err != nil {
		// The resolved token is a credential: it is persisted, never returned or
		// logged. The error cause is discarded.
		upstreamError(w, s.log(r))
		return
	}
	if _, err := s.admin.Connections.Update(r.Context(), id, map[string]any{"evolutionInstanceToken": token}); err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, struct {
		Success  bool `json:"success"`
		HasToken bool `json:"hasToken"`
	}{Success: true, HasToken: true})
}

// connectionConfigureWebhook configures the Evolution instance webhook. It
// returns no URL: the token-bearing URL is revealed only by the two
// /webhook-url endpoints (spec §6.5 R2).
func (s *server) connectionConfigureWebhook(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		BridgeURL string `json:"bridgeUrl"`
	}
	if !s.decodeOptionalJSON(w, r, &req) {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(req.BridgeURL), "/")
	if base == "" {
		base = s.admin.BridgeURL
	}
	url, err := s.webhookURL(base, "evolution", conn.ID)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	if s.admin.Upstream == nil {
		internalError(w, s.log(r), errors.New("upstream factory not wired"))
		return
	}
	evo, _, err := s.admin.Upstream.ForConnection(r.Context(), id)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	if err := evo.SetWebhook(r.Context(), url, webhookSubscribeEvents); err != nil {
		upstreamError(w, s.log(r))
		return
	}
	if _, err := s.admin.Connections.Update(r.Context(), id, map[string]any{"webhookConfigured": true}); err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, successResponse{Success: true})
}

func (s *server) connectionWebhookURLEvolution(w http.ResponseWriter, r *http.Request) {
	s.connectionWebhookURL(w, r, "evolution")
}

func (s *server) connectionWebhookURLChatwoot(w http.ResponseWriter, r *http.Request) {
	s.connectionWebhookURL(w, r, "chatwoot")
}

// connectionWebhookURL reveals the full webhook URL (which embeds the derived
// per-connection token). This is the only token-bearing response, so it is
// authenticated, marked Cache-Control: no-store, and never logged (spec §6.5 R2,
// ADR-0006 Amendment 4).
func (s *server) connectionWebhookURL(w http.ResponseWriter, r *http.Request, system string) {
	if s.adminUnavailable(w, r) {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.admin.Connections.Get(r.Context(), id); errors.Is(err, admin.ErrNotFound) {
		notFound(w, s.log(r), "connection not found")
		return
	} else if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	url, err := s.webhookURL(s.admin.BridgeURL, system, id)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(s.log(r), w, http.StatusOK, webhookURLResponse{WebhookURL: url})
}

// webhookURL builds the token-bearing webhook URL. It fails when the bridge URL
// or the token secret is not configured rather than emitting a broken URL.
func (s *server) webhookURL(base, system string, connectionID int) (string, error) {
	if base == "" {
		return "", errors.New("bridge url is not configured")
	}
	if s.admin.WebhookSecret == "" {
		return "", errors.New("webhook token secret is not configured")
	}
	token := DeriveWebhookToken(s.admin.WebhookSecret, connectionID)
	return fmt.Sprintf("%s/api/webhooks/%s/%d/%s", strings.TrimRight(base, "/"), system, connectionID, token), nil
}

// connectionUpdateValues maps a partial JSON body to whitelisted column values.
// Unknown keys are rejected. Secret fields are write-only: an empty value is
// ignored (no clear, spec §6.5 R8).
func connectionUpdateValues(raw map[string]json.RawMessage) (map[string]any, error) {
	values := make(map[string]any, len(raw))
	for key, val := range raw {
		if _, ok := admin.ConnectionColumns[key]; !ok {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		switch key {
		case "name", "evolutionApiUrl", "evolutionInstanceName", "chatwootApiUrl",
			"rejectCallMessage", "n8nWebhookUrl", "chatwootInboxName",
			"inboxGreetingMessage", "inboxOutOfOfficeMessage", "inboxTimezone",
			"inboxSenderNameType", "inboxBusinessName":
			var str string
			if err := json.Unmarshal(val, &str); err != nil {
				return nil, fmt.Errorf("field %q must be a string", key)
			}
			values[key] = str
		case "evolutionApiKey", "evolutionInstanceToken", "chatwootApiToken":
			var str string
			if err := json.Unmarshal(val, &str); err != nil {
				return nil, fmt.Errorf("field %q must be a string", key)
			}
			if str != "" {
				values[key] = str
			}
		case "chatwootAccountId", "chatwootInboxId":
			var n int
			if err := json.Unmarshal(val, &n); err != nil {
				return nil, fmt.Errorf("field %q must be an integer", key)
			}
			values[key] = n
		default:
			var b bool
			if err := json.Unmarshal(val, &b); err != nil {
				return nil, fmt.Errorf("field %q must be a boolean", key)
			}
			values[key] = b
		}
	}
	return values, nil
}

func validateConnectionCreate(in admin.ConnectionCreate) error {
	switch {
	case in.Name == "":
		return errors.New("name is required")
	case in.EvolutionAPIURL == "":
		return errors.New("evolutionApiUrl is required")
	case in.EvolutionAPIKey == "":
		return errors.New("evolutionApiKey is required")
	case in.EvolutionInstanceName == "":
		return errors.New("evolutionInstanceName is required")
	case in.ChatwootAPIURL == "":
		return errors.New("chatwootApiUrl is required")
	case in.ChatwootAPIToken == "":
		return errors.New("chatwootApiToken is required")
	case in.ChatwootAccountID <= 0:
		return errors.New("chatwootAccountId must be a positive integer")
	default:
		return nil
	}
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}
