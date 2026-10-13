package admin

import "context"

// Consumer-side ports over the upstream clients (ARCHITECTURE §2: the transport
// declares ports; cmd/bridge adapts the concrete evolution/chatwoot clients).
// The types here are bridge-owned so neither this package nor internal/httpapi
// imports the adapter packages.

// InboxSettings carries the optional inbox scalars. A nil pointer means "leave
// unchanged" and is omitted by the adapter.
type InboxSettings struct {
	GreetingEnabled            *bool
	GreetingMessage            *string
	EnableAutoAssignment       *bool
	CSATSurveyEnabled          *bool
	WorkingHoursEnabled        *bool
	OutOfOfficeMessage         *string
	Timezone                   *string
	AllowMessagesAfterResolved *bool
	LockToSingleConversation   *bool
	SenderNameType             *string
	BusinessName               *string
}

// InboxParams carries the fields for CreateInbox/UpdateInbox.
type InboxParams struct {
	Name       string
	WebhookURL string
	Settings   InboxSettings
}

// Inbox is the bridge-owned subset of a Chatwoot inbox.
type Inbox struct {
	ID          int
	Name        string
	ChannelType string
	WebhookURL  string
}

// ContactKind selects the identity-matching rule.
type ContactKind int

const (
	ContactKindUnknown ContactKind = iota
	ContactKindDirect
	ContactKindGroup
)

// ContactParams describes a contact to create.
type ContactParams struct {
	Kind      ContactKind
	Identity  string
	Name      string
	AvatarURL string
}

// Contact is the bridge-owned subset of a Chatwoot contact.
type Contact struct {
	ID          int
	Name        string
	PhoneNumber string
	Identifier  string
}

// EvolutionClient is the admin-surface port over the Evolution client. It
// exposes only the frozen operations (spec §1.3).
type EvolutionClient interface {
	ResolveInstanceToken(ctx context.Context) (string, error)
	GetInstanceStatus(ctx context.Context) (map[string]any, error)
	FetchContacts(ctx context.Context) ([]map[string]any, error)
	SetWebhook(ctx context.Context, webhookURL string, subscribe []string) error
	HealthCheck(ctx context.Context) error
}

// ChatwootClient is the admin-surface port over the Chatwoot client.
type ChatwootClient interface {
	ListInboxes(ctx context.Context) ([]Inbox, error)
	GetInbox(ctx context.Context, inboxID int) (*Inbox, error)
	CreateInbox(ctx context.Context, params InboxParams) (*Inbox, error)
	UpdateInbox(ctx context.Context, inboxID int, params InboxParams) (*Inbox, error)
	SearchContactExact(ctx context.Context, identity string, kind ContactKind) (*Contact, error)
	CreateContact(ctx context.Context, params ContactParams) (*Contact, error)
}

// UpstreamFactory builds the upstream clients for one connection on demand, so
// a connection created or edited after boot is used immediately. It also builds
// a Chatwoot client from explicit credentials for the inbox-query endpoint.
type UpstreamFactory interface {
	ForConnection(ctx context.Context, connectionID int) (EvolutionClient, ChatwootClient, error)
	ChatwootForCredentials(ctx context.Context, apiURL, apiToken string, accountID int) (ChatwootClient, error)
}
