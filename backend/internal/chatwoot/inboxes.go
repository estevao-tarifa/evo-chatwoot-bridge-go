package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// InboxSettings holds the optional inbox scalars. A nil pointer omits the key,
// matching the legacy "only include when defined" behaviour (inboxes.md §2-3).
type InboxSettings struct {
	GreetingEnabled            *bool   `json:"greeting_enabled,omitempty"`
	GreetingMessage            *string `json:"greeting_message,omitempty"`
	EnableAutoAssignment       *bool   `json:"enable_auto_assignment,omitempty"`
	CSATSurveyEnabled          *bool   `json:"csat_survey_enabled,omitempty"`
	WorkingHoursEnabled        *bool   `json:"working_hours_enabled,omitempty"`
	OutOfOfficeMessage         *string `json:"out_of_office_message,omitempty"`
	Timezone                   *string `json:"timezone,omitempty"`
	AllowMessagesAfterResolved *bool   `json:"allow_messages_after_resolved,omitempty"`
	LockToSingleConversation   *bool   `json:"lock_to_single_conversation,omitempty"`
	SenderNameType             *string `json:"sender_name_type,omitempty"`
	BusinessName               *string `json:"business_name,omitempty"`
	EnableEmailCollect         *bool   `json:"enable_email_collect,omitempty"` // update-only field
}

// InboxParams carries the fields for CreateInbox/UpdateInbox.
type InboxParams struct {
	Name       string // required by CreateInbox; sent by UpdateInbox only when non-empty
	WebhookURL string // desired value; sent only when non-empty on update
	InboxSettings
}

// Inbox is the subset of a Chatwoot inbox the bridge uses.
type Inbox struct {
	ID                         int    `json:"id"`
	Name                       string `json:"name"`
	ChannelType                string `json:"channel_type"`
	AvatarURL                  string `json:"avatar_url"`
	PhoneNumber                string `json:"phone_number"`
	CallbackWebhookURL         string `json:"callback_webhook_url"` // separate from the API-inbox webhook URL
	WebhookURL                 string `json:"webhook_url"`          // observed value, never inferred from a write
	GreetingEnabled            bool   `json:"greeting_enabled"`
	GreetingMessage            string `json:"greeting_message"`
	EnableAutoAssignment       bool   `json:"enable_auto_assignment"`
	CSATSurveyEnabled          bool   `json:"csat_survey_enabled"`
	WorkingHoursEnabled        bool   `json:"working_hours_enabled"`
	OutOfOfficeMessage         string `json:"out_of_office_message"`
	Timezone                   string `json:"timezone"`
	AllowMessagesAfterResolved bool   `json:"allow_messages_after_resolved"`
	LockToSingleConversation   bool   `json:"lock_to_single_conversation"`
	SenderNameType             string `json:"sender_name_type"`
	BusinessName               string `json:"business_name"`
}

type channelBody struct {
	Type       string `json:"type,omitempty"`
	WebhookURL string `json:"webhook_url"`
}

type createInboxBody struct {
	Name    string      `json:"name"`
	Channel channelBody `json:"channel"`
	InboxSettings
}

type updateInboxBody struct {
	Name    *string      `json:"name,omitempty"`
	Channel *channelBody `json:"channel,omitempty"`
	InboxSettings
}

type inboxesResponse struct {
	Payload []Inbox `json:"payload"`
}

func (r *inboxesResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Payload *[]Inbox `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Payload == nil {
		return errors.New("chatwoot: inboxes response missing payload")
	}
	r.Payload = *raw.Payload
	return nil
}

// ListInboxes returns every inbox in the account.
func (c *Client) ListInboxes(ctx context.Context) ([]Inbox, error) {
	var resp inboxesResponse
	if err := c.doJSON(ctx, http.MethodGet, c.accountURL("inboxes"), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Payload, nil
}

// GetInbox returns Chatwoot's observed webhook URL. Compare it to the desired
// URL after a write before treating the configuration as verified.
func (c *Client) GetInbox(ctx context.Context, inboxID int) (*Inbox, error) {
	if inboxID <= 0 {
		return nil, errors.New("chatwoot: inbox id must be positive")
	}
	var inbox Inbox
	path := c.accountURL("inboxes/" + strconv.Itoa(inboxID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &inbox); err != nil {
		return nil, err
	}
	return &inbox, nil
}

// CreateInbox creates an API-channel inbox. Its response is not readback proof.
func (c *Client) CreateInbox(ctx context.Context, params InboxParams) (*Inbox, error) {
	if strings.TrimSpace(params.Name) == "" {
		return nil, errors.New("chatwoot: inbox name is required")
	}

	body := createInboxBody{
		Name: params.Name,
		Channel: channelBody{
			Type:       "api",
			WebhookURL: params.WebhookURL,
		},
		InboxSettings: params.InboxSettings,
	}

	var inbox Inbox
	if err := c.doJSON(ctx, http.MethodPost, c.accountURL("inboxes"), body, &inbox); err != nil {
		return nil, err
	}
	return &inbox, nil
}

// UpdateInbox patches an inbox. Only non-empty Name and WebhookURL are sent;
// callers must GET to verify persistence.
func (c *Client) UpdateInbox(ctx context.Context, inboxID int, params InboxParams) (*Inbox, error) {
	if inboxID <= 0 {
		return nil, errors.New("chatwoot: inbox id must be positive")
	}

	body := updateInboxBody{InboxSettings: params.InboxSettings}
	if params.Name != "" {
		body.Name = &params.Name
	}
	if params.WebhookURL != "" {
		body.Channel = &channelBody{WebhookURL: params.WebhookURL}
	}

	var inbox Inbox
	path := c.accountURL("inboxes/" + strconv.Itoa(inboxID))
	if err := c.doJSON(ctx, http.MethodPatch, path, body, &inbox); err != nil {
		return nil, err
	}
	return &inbox, nil
}
