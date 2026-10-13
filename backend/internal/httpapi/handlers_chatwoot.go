package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

type inboxDTO struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ChannelType string `json:"channelType"`
	// WebhookURLMasked masks the upstream inbox webhook URL, which embeds the
	// bridge webhook token. The token is never returned outside the two
	// /webhook-url reveal endpoints (spec §6.5 R2).
	WebhookURLMasked string `json:"webhookUrlMasked"`
}

type inboxSettingsRequest struct {
	GreetingEnabled        *bool   `json:"greetingEnabled"`
	GreetingMessage        *string `json:"greetingMessage"`
	AutoAssignment         *bool   `json:"autoAssignment"`
	CsatEnabled            *bool   `json:"csatEnabled"`
	WorkingHoursEnabled    *bool   `json:"workingHoursEnabled"`
	OutOfOfficeMessage     *string `json:"outOfOfficeMessage"`
	Timezone               *string `json:"timezone"`
	AllowAfterResolved     *bool   `json:"allowAfterResolved"`
	LockSingleConversation *bool   `json:"lockSingleConversation"`
	SenderNameType         *string `json:"senderNameType"`
	BusinessName           *string `json:"businessName"`
}

// chatwootCredsRequest carries either explicit credentials or a connection id
// (spec §1.2: credentials move from query to body).
type chatwootCredsRequest struct {
	ChatwootAPIURL    string `json:"chatwootApiUrl"`
	ChatwootAPIToken  string `json:"chatwootApiToken"`
	ChatwootAccountID int    `json:"chatwootAccountId"`
	ConnectionID      int    `json:"connectionId"`
}

type inboxCreateRequest struct {
	chatwootCredsRequest
	InboxName  string `json:"inboxName"`
	WebhookURL string `json:"webhookUrl"`
	inboxSettingsRequest
}

type inboxSettingsUpdateRequest struct {
	Name string `json:"name"`
	inboxSettingsRequest
}

type inboxCreateResponse struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	ChannelType      string `json:"channelType"`
	WebhookURLMasked string `json:"webhookUrlMasked"`
}

type inboxSettingsResponse struct {
	Success bool     `json:"success"`
	Inbox   inboxDTO `json:"inbox"`
}

type chatwootConfigureResponse struct {
	Success bool `json:"success"`
	InboxID int  `json:"inboxId"`
}

// chatwootInboxesQuery lists the inboxes of an account (POST body credentials).
func (s *server) chatwootInboxesQuery(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var req chatwootCredsRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	client, err := s.chatwootClient(r.Context(), req)
	if err != nil {
		invalidRequest(w, s.log(r), "chatwoot credentials are incomplete")
		return
	}
	inboxes, err := client.ListInboxes(r.Context())
	if err != nil {
		upstreamError(w, s.log(r))
		return
	}
	out := make([]inboxDTO, 0, len(inboxes))
	for _, in := range inboxes {
		out = append(out, toInboxDTO(in))
	}
	writeJSON(s.log(r), w, http.StatusOK, out)
}

// chatwootInboxCreate creates an API-channel inbox and mirrors its id/name onto
// the connection when a connectionId is supplied.
func (s *server) chatwootInboxCreate(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var req inboxCreateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.InboxName) == "" {
		invalidRequest(w, s.log(r), "inboxName is required")
		return
	}
	client, err := s.chatwootClient(r.Context(), req.chatwootCredsRequest)
	if err != nil {
		invalidRequest(w, s.log(r), "chatwoot credentials are incomplete")
		return
	}
	inbox, err := client.CreateInbox(r.Context(), admin.InboxParams{
		Name:       req.InboxName,
		WebhookURL: req.WebhookURL,
		Settings:   req.inboxSettingsRequest.toAdmin(),
	})
	if err != nil {
		upstreamError(w, s.log(r))
		return
	}
	if req.ConnectionID > 0 {
		if _, err := s.admin.Connections.Update(r.Context(), req.ConnectionID, map[string]any{
			"chatwootInboxId":   inbox.ID,
			"chatwootInboxName": inbox.Name,
		}); err != nil {
			internalError(w, s.log(r), err)
			return
		}
	}
	writeJSON(s.log(r), w, http.StatusCreated, inboxCreateResponse{
		ID:               inbox.ID,
		Name:             inbox.Name,
		ChannelType:      inbox.ChannelType,
		WebhookURLMasked: maskURLToken(inbox.WebhookURL),
	})
}

// chatwootInboxSettings patches an inbox and mirrors the settings onto the
// connection row (legacy dual-write).
func (s *server) chatwootInboxSettings(w http.ResponseWriter, r *http.Request) {
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
	var req inboxSettingsUpdateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	client, err := s.connectionChatwoot(r.Context(), id)
	if err != nil {
		invalidRequest(w, s.log(r), "connection has no usable chatwoot credentials")
		return
	}
	if conn.ChatwootInboxID <= 0 {
		invalidRequest(w, s.log(r), "connection has no chatwoot inbox")
		return
	}
	inbox, err := client.UpdateInbox(r.Context(), conn.ChatwootInboxID, admin.InboxParams{
		Name:     req.Name,
		Settings: req.inboxSettingsRequest.toAdmin(),
	})
	if err != nil {
		upstreamError(w, s.log(r))
		return
	}
	if values := req.inboxSettingsRequest.toConnectionValues(); len(values) > 0 {
		if _, err := s.admin.Connections.Update(r.Context(), id, values); err != nil {
			internalError(w, s.log(r), err)
			return
		}
	}
	writeJSON(s.log(r), w, http.StatusOK, inboxSettingsResponse{Success: true, Inbox: toInboxDTO(*inbox)})
}

// chatwootConfigureWebhook points the Chatwoot inbox webhook at the bridge. Like
// the Evolution configure route it returns no URL (spec §6.5 R2).
func (s *server) chatwootConfigureWebhook(w http.ResponseWriter, r *http.Request) {
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
	url, err := s.webhookURL(base, "chatwoot", conn.ID)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	client, err := s.connectionChatwoot(r.Context(), id)
	if err != nil {
		invalidRequest(w, s.log(r), "connection has no usable chatwoot credentials")
		return
	}
	if _, err := client.UpdateInbox(r.Context(), conn.ChatwootInboxID, admin.InboxParams{WebhookURL: url}); err != nil {
		upstreamError(w, s.log(r))
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, chatwootConfigureResponse{Success: true, InboxID: conn.ChatwootInboxID})
}

// chatwootClient builds a client from explicit credentials or a connection id.
func (s *server) chatwootClient(ctx context.Context, req chatwootCredsRequest) (admin.ChatwootClient, error) {
	if req.ConnectionID > 0 {
		return s.connectionChatwoot(ctx, req.ConnectionID)
	}
	if s.admin.Upstream == nil {
		return nil, errors.New("upstream factory not wired")
	}
	return s.admin.Upstream.ChatwootForCredentials(ctx, req.ChatwootAPIURL, req.ChatwootAPIToken, req.ChatwootAccountID)
}

// connectionChatwoot builds the Chatwoot client for a connection via the factory.
func (s *server) connectionChatwoot(ctx context.Context, connectionID int) (admin.ChatwootClient, error) {
	if s.admin.Upstream == nil {
		return nil, errors.New("upstream factory not wired")
	}
	_, cw, err := s.admin.Upstream.ForConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if cw == nil {
		return nil, errors.New("chatwoot client unavailable")
	}
	return cw, nil
}

func toInboxDTO(in admin.Inbox) inboxDTO {
	return inboxDTO{
		ID:               in.ID,
		Name:             in.Name,
		ChannelType:      in.ChannelType,
		WebhookURLMasked: maskURLToken(in.WebhookURL),
	}
}

func (r inboxSettingsRequest) toAdmin() admin.InboxSettings {
	return admin.InboxSettings{
		GreetingEnabled:            r.GreetingEnabled,
		GreetingMessage:            r.GreetingMessage,
		EnableAutoAssignment:       r.AutoAssignment,
		CSATSurveyEnabled:          r.CsatEnabled,
		WorkingHoursEnabled:        r.WorkingHoursEnabled,
		OutOfOfficeMessage:         r.OutOfOfficeMessage,
		Timezone:                   r.Timezone,
		AllowMessagesAfterResolved: r.AllowAfterResolved,
		LockToSingleConversation:   r.LockSingleConversation,
		SenderNameType:             r.SenderNameType,
		BusinessName:               r.BusinessName,
	}
}

func (r inboxSettingsRequest) toConnectionValues() map[string]any {
	values := map[string]any{}
	if r.GreetingEnabled != nil {
		values["inboxGreetingEnabled"] = *r.GreetingEnabled
	}
	if r.GreetingMessage != nil {
		values["inboxGreetingMessage"] = *r.GreetingMessage
	}
	if r.AutoAssignment != nil {
		values["inboxAutoAssignment"] = *r.AutoAssignment
	}
	if r.CsatEnabled != nil {
		values["inboxCsatEnabled"] = *r.CsatEnabled
	}
	if r.WorkingHoursEnabled != nil {
		values["inboxWorkingHoursEnabled"] = *r.WorkingHoursEnabled
	}
	if r.OutOfOfficeMessage != nil {
		values["inboxOutOfOfficeMessage"] = *r.OutOfOfficeMessage
	}
	if r.Timezone != nil {
		values["inboxTimezone"] = *r.Timezone
	}
	if r.AllowAfterResolved != nil {
		values["inboxAllowAfterResolved"] = *r.AllowAfterResolved
	}
	if r.LockSingleConversation != nil {
		values["inboxLockSingleConversation"] = *r.LockSingleConversation
	}
	if r.SenderNameType != nil {
		values["inboxSenderNameType"] = *r.SenderNameType
	}
	if r.BusinessName != nil {
		values["inboxBusinessName"] = *r.BusinessName
	}
	return values
}
