package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// decodeJSON decodes the request body into dst, mapping a malformed body to 400
// and a body over the 256 KiB cap to 413. It returns false when it has already
// written the error.
func (s *server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			requestTooLarge(w, s.log(r))
			return false
		}
		invalidRequest(w, s.log(r), "invalid json body")
		return false
	}
	return true
}

// decodeOptionalJSON decodes an optional body: an empty body is not an error
// (the field stays at its zero value).
func (s *server) decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	return s.decodeJSON(w, r, dst)
}

// pathID parses the {id} path value. It returns false (after writing 404) when
// the value is absent or not a positive int.
func (s *server) pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		notFound(w, s.log(r), "not found")
		return 0, false
	}
	return id, true
}

// pagination bounds (spec §1.1): limit 1..100, offset >= 0; out of range is a
// 400, never a silent clamp.
const (
	maxPageLimit = 100
)

// parsePagination reads limit/offset, applying the route default. A
// non-integer, out-of-range or negative value is an error.
func parsePagination(r *http.Request, defaultLimit int) (limit, offset int, err error) {
	limit = defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxPageLimit {
			return 0, 0, fmt.Errorf("limit must be an integer between 1 and %d", maxPageLimit)
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

// maskSecret renders a secret as a fixed marker plus at most its last four
// runes, so a readback confirms a value is set without disclosing it. A value
// of four runes or fewer is fully masked.
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}

// connectionDTO is the admin view of a connection. Secret fields are never
// serialised: only has*/masked markers (spec §1.5, ADR-0006 Amendment 4).
type connectionDTO struct {
	ID                           int       `json:"id"`
	Name                         string    `json:"name"`
	EvolutionAPIURL              string    `json:"evolutionApiUrl"`
	HasEvolutionAPIKey           bool      `json:"hasEvolutionApiKey"`
	EvolutionAPIKeyMasked        string    `json:"evolutionApiKeyMasked,omitempty"`
	EvolutionInstanceName        string    `json:"evolutionInstanceName"`
	HasEvolutionInstanceToken    bool      `json:"hasEvolutionInstanceToken"`
	EvolutionInstanceTokenMasked string    `json:"evolutionInstanceTokenMasked,omitempty"`
	ChatwootAPIURL               string    `json:"chatwootApiUrl"`
	HasChatwootAPIToken          bool      `json:"hasChatwootApiToken"`
	ChatwootAPITokenMasked       string    `json:"chatwootApiTokenMasked,omitempty"`
	ChatwootAccountID            int       `json:"chatwootAccountId"`
	ChatwootInboxID              int       `json:"chatwootInboxId"`
	ChatwootInboxName            string    `json:"chatwootInboxName"`
	IsActive                     bool      `json:"isActive"`
	IgnoreGroups                 bool      `json:"ignoreGroups"`
	SignMessages                 bool      `json:"signMessages"`
	AutoReadMessages             bool      `json:"autoReadMessages"`
	RejectCalls                  bool      `json:"rejectCalls"`
	RejectCallMessage            string    `json:"rejectCallMessage"`
	N8NWebhookURL                string    `json:"n8nWebhookUrl"`
	WebhookConfigured            bool      `json:"webhookConfigured"`
	InboxGreetingEnabled         bool      `json:"inboxGreetingEnabled"`
	InboxGreetingMessage         string    `json:"inboxGreetingMessage"`
	InboxAutoAssignment          bool      `json:"inboxAutoAssignment"`
	InboxCsatEnabled             bool      `json:"inboxCsatEnabled"`
	InboxWorkingHoursEnabled     bool      `json:"inboxWorkingHoursEnabled"`
	InboxOutOfOfficeMessage      string    `json:"inboxOutOfOfficeMessage"`
	InboxTimezone                string    `json:"inboxTimezone"`
	InboxAllowAfterResolved      bool      `json:"inboxAllowAfterResolved"`
	InboxLockSingleConversation  bool      `json:"inboxLockSingleConversation"`
	InboxSenderNameType          string    `json:"inboxSenderNameType"`
	InboxBusinessName            string    `json:"inboxBusinessName"`
	CreatedAt                    time.Time `json:"createdAt"`
	UpdatedAt                    time.Time `json:"updatedAt"`
}

// toConnectionDTO is the single constructor for the connection DTO, so no new
// field can accidentally serialise a secret.
func toConnectionDTO(c admin.Connection) connectionDTO {
	return connectionDTO{
		ID:                           c.ID,
		Name:                         c.Name,
		EvolutionAPIURL:              c.EvolutionAPIURL,
		HasEvolutionAPIKey:           c.EvolutionAPIKey != "",
		EvolutionAPIKeyMasked:        maskSecret(c.EvolutionAPIKey),
		EvolutionInstanceName:        c.EvolutionInstanceName,
		HasEvolutionInstanceToken:    c.EvolutionInstanceToken != "",
		EvolutionInstanceTokenMasked: maskSecret(c.EvolutionInstanceToken),
		ChatwootAPIURL:               c.ChatwootAPIURL,
		HasChatwootAPIToken:          c.ChatwootAPIToken != "",
		ChatwootAPITokenMasked:       maskSecret(c.ChatwootAPIToken),
		ChatwootAccountID:            c.ChatwootAccountID,
		ChatwootInboxID:              c.ChatwootInboxID,
		ChatwootInboxName:            c.ChatwootInboxName,
		IsActive:                     c.IsActive,
		IgnoreGroups:                 c.IgnoreGroups,
		SignMessages:                 c.SignMessages,
		AutoReadMessages:             c.AutoReadMessages,
		RejectCalls:                  c.RejectCalls,
		RejectCallMessage:            c.RejectCallMessage,
		N8NWebhookURL:                c.N8NWebhookURL,
		WebhookConfigured:            c.WebhookConfigured,
		InboxGreetingEnabled:         c.InboxGreetingEnabled,
		InboxGreetingMessage:         c.InboxGreetingMessage,
		InboxAutoAssignment:          c.InboxAutoAssignment,
		InboxCsatEnabled:             c.InboxCsatEnabled,
		InboxWorkingHoursEnabled:     c.InboxWorkingHours,
		InboxOutOfOfficeMessage:      c.InboxOutOfOffice,
		InboxTimezone:                c.InboxTimezone,
		InboxAllowAfterResolved:      c.InboxAllowAfterResolve,
		InboxLockSingleConversation:  c.InboxLockSingle,
		InboxSenderNameType:          c.InboxSenderNameType,
		InboxBusinessName:            c.InboxBusinessName,
		CreatedAt:                    c.CreatedAt,
		UpdatedAt:                    c.UpdatedAt,
	}
}

// logDTO is one message_logs row for the SPA. rawPayload is never present.
type logDTO struct {
	ID                   int64      `json:"id"`
	ConnectionID         int        `json:"connectionId"`
	Direction            string     `json:"direction"`
	MessageType          string     `json:"messageType"`
	Status               string     `json:"status"`
	SourceMessageID      string     `json:"sourceMessageId"`
	DestinationMessageID string     `json:"destinationMessageId"`
	ContactPhone         string     `json:"contactPhone"`
	ContactName          string     `json:"contactName"`
	ContentPreview       string     `json:"contentPreview"`
	ErrorMessage         string     `json:"errorMessage"`
	CreatedAt            time.Time  `json:"createdAt"`
	ProcessedAt          *time.Time `json:"processedAt"`
}

func toLogDTO(l admin.LogRow) logDTO {
	return logDTO{
		ID:                   l.ID,
		ConnectionID:         l.ConnectionID,
		Direction:            l.Direction,
		MessageType:          l.MessageType,
		Status:               l.Status,
		SourceMessageID:      l.SourceMessageID,
		DestinationMessageID: l.DestinationMessageID,
		ContactPhone:         l.ContactPhone,
		ContactName:          l.ContactName,
		ContentPreview:       l.ContentPreview,
		ErrorMessage:         l.ErrorMessage,
		CreatedAt:            l.CreatedAt,
		ProcessedAt:          l.ProcessedAt,
	}
}

// notificationSettingsDTO masks smtpPass (write-only, spec §1.5).
type notificationSettingsDTO struct {
	ID                   int    `json:"id"`
	Enabled              bool   `json:"enabled"`
	FailureThreshold     int    `json:"failureThreshold"`
	CooldownMinutes      int    `json:"cooldownMinutes"`
	WhatsappEnabled      bool   `json:"whatsappEnabled"`
	WhatsappConnectionID int    `json:"whatsappConnectionId"`
	WhatsappAlertPhone   string `json:"whatsappAlertPhone"`
	EmailEnabled         bool   `json:"emailEnabled"`
	SMTPHost             string `json:"smtpHost"`
	SMTPPort             int    `json:"smtpPort"`
	SMTPUser             string `json:"smtpUser"`
	HasSMTPPass          bool   `json:"hasSmtpPass"`
	SMTPPassMasked       string `json:"smtpPassMasked,omitempty"`
	SMTPSecure           bool   `json:"smtpSecure"`
	EmailFrom            string `json:"emailFrom"`
	EmailTo              string `json:"emailTo"`
}

func toNotificationSettingsDTO(n admin.NotificationSettings) notificationSettingsDTO {
	return notificationSettingsDTO{
		ID:                   n.ID,
		Enabled:              n.Enabled,
		FailureThreshold:     n.FailureThreshold,
		CooldownMinutes:      n.CooldownMinutes,
		WhatsappEnabled:      n.WhatsappEnabled,
		WhatsappConnectionID: n.WhatsappConnectionID,
		WhatsappAlertPhone:   n.WhatsappAlertPhone,
		EmailEnabled:         n.EmailEnabled,
		SMTPHost:             n.SMTPHost,
		SMTPPort:             n.SMTPPort,
		SMTPUser:             n.SMTPUser,
		HasSMTPPass:          n.HasSMTPPass,
		SMTPPassMasked:       maskSecret(n.SMTPPass),
		SMTPSecure:           n.SMTPSecure,
		EmailFrom:            n.EmailFrom,
		EmailTo:              n.EmailTo,
	}
}

// alertDTO is one notification_alerts row.
type alertDTO struct {
	ID           int64     `json:"id"`
	ConnectionID int       `json:"connectionId"`
	Channel      string    `json:"channel"`
	AlertType    string    `json:"alertType"`
	Message      string    `json:"message"`
	Success      bool      `json:"success"`
	ErrorMessage string    `json:"errorMessage"`
	FailureCount int       `json:"failureCount"`
	CreatedAt    time.Time `json:"createdAt"`
}

func toAlertDTO(a admin.Alert) alertDTO {
	return alertDTO{
		ID:           a.ID,
		ConnectionID: a.ConnectionID,
		Channel:      a.Channel,
		AlertType:    a.AlertType,
		Message:      a.Message,
		Success:      a.Success,
		ErrorMessage: a.ErrorMessage,
		FailureCount: a.FailureCount,
		CreatedAt:    a.CreatedAt,
	}
}
