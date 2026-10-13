// Package admin holds the bridge-owned data types and consumer-side ports for
// the Phase 5 admin REST surface (spec §1). The HTTP layer (internal/httpapi)
// depends on these ports; internal/storage/mysql implements them. No SQL and no
// HTTP types live here.
//
// Secret fields (Evolution/Chatwoot credentials, SMTP password) are carried so
// the storage layer can read them, but the HTTP layer never serialises them:
// the DTO exposes only has*/masked markers (spec §1.5, ADR-0006 Amendment 4).
package admin

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports an absent row. Storage adapters return it instead of a
// zero value so the HTTP layer can answer 404.
var ErrNotFound = errors.New("admin: not found")

// Connection is the full bridge-owned view of a `connections` row.
type Connection struct {
	ID                     int
	Name                   string
	EvolutionAPIURL        string
	EvolutionAPIKey        string // secret
	EvolutionInstanceName  string
	EvolutionInstanceToken string // secret
	ChatwootAPIURL         string
	ChatwootAPIToken       string // secret
	ChatwootAccountID      int
	ChatwootInboxID        int
	ChatwootInboxName      string
	IsActive               bool
	IgnoreGroups           bool
	SignMessages           bool
	AutoReadMessages       bool
	RejectCalls            bool
	RejectCallMessage      string
	N8NWebhookURL          string
	WebhookConfigured      bool
	InboxGreetingEnabled   bool
	InboxGreetingMessage   string
	InboxAutoAssignment    bool
	InboxCsatEnabled       bool
	InboxWorkingHours      bool
	InboxOutOfOffice       string
	InboxTimezone          string
	InboxAllowAfterResolve bool
	InboxLockSingle        bool
	InboxSenderNameType    string
	InboxBusinessName      string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ConnectionCreate carries the required fields for a new connection. Optional
// booleans are applied as given; absent JSON fields use the legacy defaults
// (ignoreGroups=true).
type ConnectionCreate struct {
	Name                   string
	EvolutionAPIURL        string
	EvolutionAPIKey        string
	EvolutionInstanceName  string
	EvolutionInstanceToken string
	ChatwootAPIURL         string
	ChatwootAPIToken       string
	ChatwootAccountID      int
	ChatwootInboxID        int
	IgnoreGroups           bool
	SignMessages           bool
	AutoReadMessages       bool
	RejectCalls            bool
	RejectCallMessage      string
	N8NWebhookURL          string
}

// ConnectionColumns maps the canonical partial-update field names to their
// legacy camelCase column names. The storage adapter refuses any key not in this
// map, so a dynamic SET clause can never inject SQL.
var ConnectionColumns = map[string]string{
	"name":                        "name",
	"evolutionApiUrl":             "evolutionApiUrl",
	"evolutionApiKey":             "evolutionApiKey",
	"evolutionInstanceName":       "evolutionInstanceName",
	"evolutionInstanceToken":      "evolutionInstanceToken",
	"chatwootApiUrl":              "chatwootApiUrl",
	"chatwootApiToken":            "chatwootApiToken",
	"chatwootAccountId":           "chatwootAccountId",
	"chatwootInboxId":             "chatwootInboxId",
	"chatwootInboxName":           "chatwootInboxName",
	"isActive":                    "isActive",
	"ignoreGroups":                "ignoreGroups",
	"signMessages":                "signMessages",
	"autoReadMessages":            "autoReadMessages",
	"rejectCalls":                 "rejectCalls",
	"rejectCallMessage":           "rejectCallMessage",
	"n8nWebhookUrl":               "n8nWebhookUrl",
	"webhookConfigured":           "webhookConfigured",
	"inboxGreetingEnabled":        "inboxGreetingEnabled",
	"inboxGreetingMessage":        "inboxGreetingMessage",
	"inboxAutoAssignment":         "inboxAutoAssignment",
	"inboxCsatEnabled":            "inboxCsatEnabled",
	"inboxWorkingHoursEnabled":    "inboxWorkingHoursEnabled",
	"inboxOutOfOfficeMessage":     "inboxOutOfOfficeMessage",
	"inboxTimezone":               "inboxTimezone",
	"inboxAllowAfterResolved":     "inboxAllowAfterResolved",
	"inboxLockSingleConversation": "inboxLockSingleConversation",
	"inboxSenderNameType":         "inboxSenderNameType",
	"inboxBusinessName":           "inboxBusinessName",
}

// ConnectionsStore is the admin CRUD port over `connections`. Deactivate is a
// soft delete: it sets isActive=false and preserves contacts_map/message_logs
// (spec §6.5 R7).
type ConnectionsStore interface {
	List(ctx context.Context) ([]Connection, error)
	Get(ctx context.Context, id int) (*Connection, error)
	Create(ctx context.Context, in ConnectionCreate) (*Connection, error)
	Update(ctx context.Context, id int, values map[string]any) (*Connection, error)
	Deactivate(ctx context.Context, id int) error
}

// LogRow is one `message_logs` row. RawPayload is deliberately absent: it is
// never read or returned (spec §1.5).
type LogRow struct {
	ID                   int64
	ConnectionID         int
	Direction            string
	MessageType          string
	Status               string
	SourceMessageID      string
	DestinationMessageID string
	ContactPhone         string
	ContactName          string
	ContentPreview       string
	ErrorMessage         string
	CreatedAt            time.Time
	ProcessedAt          *time.Time
}

// LogQuery filters and pages the log list. Limit/Offset are already validated.
type LogQuery struct {
	ConnectionID int
	Direction    string
	Status       string
	Search       string
	Limit        int
	Offset       int
}

// LogsStore is the read port over `message_logs`.
type LogsStore interface {
	List(ctx context.Context, q LogQuery) ([]LogRow, int, error)
}

// Stats is the dashboard summary.
type Stats struct {
	TotalConnections  int
	ActiveConnections int
	TotalMessages     int64
	FailedMessages    int64
	TodayMessages     int64
	RecentLogs        []LogRow
}

// Bucket is one dashboard time bucket. Label is the hour ("14:00") or day
// ("2026-10-12").
type Bucket struct {
	Label    string
	Incoming int64
	Outgoing int64
	Total    int64
}

// TypeCount is one message-type tally.
type TypeCount struct {
	Type  string
	Count int64
}

// DashboardStore is the read port over the dashboard aggregates.
type DashboardStore interface {
	Stats(ctx context.Context) (Stats, error)
	MessagesPerHour(ctx context.Context) ([]Bucket, error)
	MessagesPerDay(ctx context.Context) ([]Bucket, error)
	MessageTypes(ctx context.Context) ([]TypeCount, error)
}

// SettingsStore is the read port over the baseline `bridge_settings` KV table.
type SettingsStore interface {
	// BridgeURL returns the stored bridge URL, or "" when absent. A missing
	// table (a legacy install that never ran the baseline) is not an error.
	BridgeURL(ctx context.Context) (string, error)
}

// NotificationSettings mirrors the single `notification_settings` row. SMTPPass
// is a secret; HasSMTPPass is derived for the DTO.
type NotificationSettings struct {
	ID                   int
	Enabled              bool
	FailureThreshold     int
	CooldownMinutes      int
	WhatsappEnabled      bool
	WhatsappConnectionID int
	WhatsappAlertPhone   string
	EmailEnabled         bool
	SMTPHost             string
	SMTPPort             int
	SMTPUser             string
	SMTPPass             string // secret
	HasSMTPPass          bool
	SMTPSecure           bool
	EmailFrom            string
	EmailTo              string
}

// Alert is one `notification_alerts` row.
type Alert struct {
	ID           int64
	ConnectionID int
	Channel      string
	AlertType    string
	Message      string
	Success      bool
	ErrorMessage string
	FailureCount int
	CreatedAt    time.Time
}

// AlertQuery filters and pages the alert list.
type AlertQuery struct {
	ConnectionID int
	Limit        int
	Offset       int
}

// NotificationColumns maps the partial-update field names to their columns.
var NotificationColumns = map[string]string{
	"enabled":              "enabled",
	"failureThreshold":     "failureThreshold",
	"cooldownMinutes":      "cooldownMinutes",
	"whatsappEnabled":      "whatsappEnabled",
	"whatsappConnectionId": "whatsappConnectionId",
	"whatsappAlertPhone":   "whatsappAlertPhone",
	"emailEnabled":         "emailEnabled",
	"smtpHost":             "smtpHost",
	"smtpPort":             "smtpPort",
	"smtpUser":             "smtpUser",
	"smtpPass":             "smtpPass",
	"smtpSecure":           "smtpSecure",
	"emailFrom":            "emailFrom",
	"emailTo":              "emailTo",
}

// NotificationsStore is the read/write port over notification_settings and
// notification_alerts.
type NotificationsStore interface {
	GetSettings(ctx context.Context) (*NotificationSettings, error)
	UpdateSettings(ctx context.Context, values map[string]any) (*NotificationSettings, error)
	ListAlerts(ctx context.Context, q AlertQuery) ([]Alert, int, error)
}
