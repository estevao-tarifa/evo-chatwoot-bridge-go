package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"

	"golang.org/x/crypto/bcrypt"
)

// adminState is the shared in-memory fixture behind the five admin store ports.
// The ports have overlapping method names (List), so each is exposed through a
// distinct thin adapter type instead of one struct.
type adminState struct {
	mu          sync.Mutex
	connections []admin.Connection
	nextConnID  int
	logs        []admin.LogRow
	logsTotal   int
	stats       admin.Stats
	hours       []admin.Bucket
	days        []admin.Bucket
	types       []admin.TypeCount
	settings    *admin.NotificationSettings
	alerts      []admin.Alert
	alertsTotal int
	updateCalls []map[string]any
}

func newAdminState() *adminState {
	return &adminState{
		nextConnID: 1,
		settings:   &admin.NotificationSettings{ID: 1, FailureThreshold: 3, CooldownMinutes: 30, SMTPPort: 587},
	}
}

func (s *adminState) Connections() admin.ConnectionsStore { return fakeConnections{s} }
func (s *adminState) Logs() admin.LogsStore               { return fakeLogs{s} }
func (s *adminState) Dashboard() admin.DashboardStore     { return fakeDashboard{s} }
func (s *adminState) Settings() admin.SettingsStore       { return fakeSettings{} }
func (s *adminState) Notifications() admin.NotificationsStore {
	return fakeNotifications{s}
}

type fakeConnections struct{ s *adminState }

func (f fakeConnections) List(context.Context) ([]admin.Connection, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return append([]admin.Connection{}, f.s.connections...), nil
}

func (f fakeConnections) Get(_ context.Context, id int) (*admin.Connection, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for i := range f.s.connections {
		if f.s.connections[i].ID == id {
			clone := f.s.connections[i]
			return &clone, nil
		}
	}
	return nil, nil
}

func (f fakeConnections) Create(_ context.Context, in admin.ConnectionCreate) (*admin.Connection, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	c := admin.Connection{
		ID: f.s.nextConnID, Name: in.Name, EvolutionAPIURL: in.EvolutionAPIURL,
		EvolutionAPIKey: in.EvolutionAPIKey, EvolutionInstanceName: in.EvolutionInstanceName,
		EvolutionInstanceToken: in.EvolutionInstanceToken, ChatwootAPIURL: in.ChatwootAPIURL,
		ChatwootAPIToken: in.ChatwootAPIToken, ChatwootAccountID: in.ChatwootAccountID,
		ChatwootInboxID: in.ChatwootInboxID, IgnoreGroups: in.IgnoreGroups,
		SignMessages: in.SignMessages, AutoReadMessages: in.AutoReadMessages,
		RejectCalls: in.RejectCalls, RejectCallMessage: in.RejectCallMessage,
		N8NWebhookURL: in.N8NWebhookURL, IsActive: true,
	}
	f.s.nextConnID++
	f.s.connections = append(f.s.connections, c)
	clone := c
	return &clone, nil
}

func (f fakeConnections) Update(_ context.Context, id int, values map[string]any) (*admin.Connection, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.updateCalls = append(f.s.updateCalls, values)
	for i := range f.s.connections {
		if f.s.connections[i].ID != id {
			continue
		}
		applyConnectionValues(&f.s.connections[i], values)
		clone := f.s.connections[i]
		return &clone, nil
	}
	return nil, admin.ErrNotFound
}

func (f fakeConnections) Deactivate(_ context.Context, id int) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for i := range f.s.connections {
		if f.s.connections[i].ID == id {
			f.s.connections[i].IsActive = false
			return nil
		}
	}
	return admin.ErrNotFound
}

type fakeLogs struct{ s *adminState }

func (f fakeLogs) List(_ context.Context, _ admin.LogQuery) ([]admin.LogRow, int, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return append([]admin.LogRow{}, f.s.logs...), f.s.logsTotal, nil
}

type fakeDashboard struct{ s *adminState }

func (f fakeDashboard) Stats(context.Context) (admin.Stats, error) { return f.s.stats, nil }
func (f fakeDashboard) MessagesPerHour(context.Context) ([]admin.Bucket, error) {
	return f.s.hours, nil
}
func (f fakeDashboard) MessagesPerDay(context.Context) ([]admin.Bucket, error) {
	return f.s.days, nil
}
func (f fakeDashboard) MessageTypes(context.Context) ([]admin.TypeCount, error) {
	return f.s.types, nil
}

type fakeSettings struct{}

func (fakeSettings) BridgeURL(context.Context) (string, error) { return "", nil }

type fakeNotifications struct{ s *adminState }

func (f fakeNotifications) GetSettings(context.Context) (*admin.NotificationSettings, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.settings == nil {
		return nil, nil
	}
	clone := *f.s.settings
	return &clone, nil
}

func (f fakeNotifications) UpdateSettings(_ context.Context, values map[string]any) (*admin.NotificationSettings, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.settings == nil {
		f.s.settings = &admin.NotificationSettings{}
	}
	applyNotificationValues(f.s.settings, values)
	clone := *f.s.settings
	return &clone, nil
}

func (f fakeNotifications) ListAlerts(context.Context, admin.AlertQuery) ([]admin.Alert, int, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return f.s.alerts, f.s.alertsTotal, nil
}

// applyConnectionValues mirrors the MySQL adapter's field→column mapping for the
// handful of fields the tests exercise.
func applyConnectionValues(c *admin.Connection, values map[string]any) {
	for key, value := range values {
		switch key {
		case "name":
			c.Name, _ = value.(string)
		case "evolutionInstanceToken":
			c.EvolutionInstanceToken, _ = value.(string)
		case "webhookConfigured":
			c.WebhookConfigured, _ = value.(bool)
		case "chatwootInboxId":
			c.ChatwootInboxID, _ = value.(int)
		case "chatwootInboxName":
			c.ChatwootInboxName, _ = value.(string)
		case "inboxGreetingEnabled":
			c.InboxGreetingEnabled, _ = value.(bool)
		}
	}
}

func applyNotificationValues(n *admin.NotificationSettings, values map[string]any) {
	for key, value := range values {
		switch key {
		case "enabled":
			n.Enabled, _ = value.(bool)
		case "smtpPass":
			n.SMTPPass, _ = value.(string)
			n.HasSMTPPass = true
		}
	}
}

// ─── auth store fake ──────────────────────────────────────────────────────────

type fakeAuthStore struct {
	mu        sync.Mutex
	nextID    int
	rows      []*auth.Admin
	createErr error
}

func newFakeAuthStore() *fakeAuthStore { return &fakeAuthStore{nextID: 1} }

func (f *fakeAuthStore) Count(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows), nil
}

func (f *fakeAuthStore) FindByUsername(_ context.Context, username string) (*auth.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.rows {
		if a.Username == username {
			clone := *a
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeAuthStore) FindByID(_ context.Context, id int) (*auth.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.rows {
		if a.ID == id {
			clone := *a
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeAuthStore) Create(_ context.Context, username, hash, displayName string) (*auth.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	a := &auth.Admin{ID: f.nextID, Username: username, PasswordHash: hash, DisplayName: displayName, IsActive: true}
	f.nextID++
	f.rows = append(f.rows, a)
	clone := *a
	return &clone, nil
}

func (f *fakeAuthStore) TouchLastLogin(context.Context, int) error { return nil }

// seed adds an admin with a bcrypt hash of password.
func (f *fakeAuthStore) seed(username, password string, active bool) *auth.Admin {
	hash, _ := bcryptGenerate(password)
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &auth.Admin{ID: f.nextID, Username: username, PasswordHash: hash, IsActive: active}
	f.nextID++
	f.rows = append(f.rows, a)
	return a
}

// ─── upstream fakes ───────────────────────────────────────────────────────────

type fakeUpstream struct {
	evo         *fakeEvolution
	cw          *fakeChatwoot
	credsClient admin.ChatwootClient
	forConnErr  error
}

func newFakeUpstream() *fakeUpstream {
	return &fakeUpstream{evo: &fakeEvolution{}, cw: newFakeChatwoot()}
}

func (f *fakeUpstream) ForConnection(context.Context, int) (admin.EvolutionClient, admin.ChatwootClient, error) {
	if f.forConnErr != nil {
		return nil, nil, f.forConnErr
	}
	return f.evo, f.cw, nil
}

func (f *fakeUpstream) ChatwootForCredentials(context.Context, string, string, int) (admin.ChatwootClient, error) {
	if f.credsClient != nil {
		return f.credsClient, nil
	}
	return f.cw, nil
}

type fakeEvolution struct {
	resolvedToken string
	resolveErr    error
	webhookURL    string
	subscribe     []string
	status        map[string]any
	statusErr     error
	contacts      []map[string]any
	contactsErr   error
	healthErr     error
}

func (f *fakeEvolution) ResolveInstanceToken(context.Context) (string, error) {
	return f.resolvedToken, f.resolveErr
}
func (f *fakeEvolution) GetInstanceStatus(context.Context) (map[string]any, error) {
	return f.status, f.statusErr
}
func (f *fakeEvolution) FetchContacts(context.Context) ([]map[string]any, error) {
	return f.contacts, f.contactsErr
}
func (f *fakeEvolution) SetWebhook(_ context.Context, url string, subscribe []string) error {
	f.webhookURL = url
	f.subscribe = subscribe
	return nil
}
func (f *fakeEvolution) HealthCheck(context.Context) error { return f.healthErr }

type fakeChatwoot struct {
	inboxes      []admin.Inbox
	created      *admin.Inbox
	updated      *admin.Inbox
	updateParams admin.InboxParams
	contacts     map[string]*admin.Contact
	// inboxWebhookURL, when set, emulates the token-bearing webhook URL
	// Chatwoot returns on an inbox create/update (a leak the DTO must mask).
	inboxWebhookURL string
	createErr       error
	searchErr       error
	listErr         error
}

func newFakeChatwoot() *fakeChatwoot {
	return &fakeChatwoot{contacts: map[string]*admin.Contact{}}
}

func (f *fakeChatwoot) ListInboxes(context.Context) ([]admin.Inbox, error) {
	return f.inboxes, f.listErr
}
func (f *fakeChatwoot) GetInbox(_ context.Context, id int) (*admin.Inbox, error) {
	for i := range f.inboxes {
		if f.inboxes[i].ID == id {
			clone := f.inboxes[i]
			return &clone, nil
		}
	}
	return nil, errors.New("not found")
}
func (f *fakeChatwoot) CreateInbox(_ context.Context, params admin.InboxParams) (*admin.Inbox, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	inbox := admin.Inbox{ID: 42, Name: params.Name, ChannelType: "api", WebhookURL: f.inboxWebhook(params.WebhookURL)}
	f.created = &inbox
	return &inbox, nil
}
func (f *fakeChatwoot) UpdateInbox(_ context.Context, id int, params admin.InboxParams) (*admin.Inbox, error) {
	f.updateParams = params
	inbox := admin.Inbox{ID: id, Name: params.Name, ChannelType: "api", WebhookURL: f.inboxWebhook(params.WebhookURL)}
	f.updated = &inbox
	return &inbox, nil
}

// inboxWebhook emulates Chatwoot returning the inbox's current webhook URL: the
// seeded token-bearing value when set, else the value just written.
func (f *fakeChatwoot) inboxWebhook(written string) string {
	if f.inboxWebhookURL != "" {
		return f.inboxWebhookURL
	}
	return written
}
func (f *fakeChatwoot) SearchContactExact(_ context.Context, identity string, _ admin.ContactKind) (*admin.Contact, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.contacts[identity], nil
}
func (f *fakeChatwoot) CreateContact(_ context.Context, params admin.ContactParams) (*admin.Contact, error) {
	contact := &admin.Contact{ID: len(f.contacts) + 1, Name: params.Name, Identifier: params.Identity}
	f.contacts[params.Identity] = contact
	return contact, nil
}

// bcryptGenerate hashes a synthetic password at the configured cost.
func bcryptGenerate(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), auth.BcryptCost)
	return string(hash), err
}

// testClock is a manually advanced clock for the auth service.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
