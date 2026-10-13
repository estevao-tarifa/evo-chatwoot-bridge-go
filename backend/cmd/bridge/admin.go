package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/chatwoot"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/config"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/evolution"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/httpapi"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/storage/mysql"

	"golang.org/x/crypto/bcrypt"
)

// upstreamFactory builds per-connection clients on demand from the connections
// row, so a connection created or edited after boot is used immediately. It
// adapts the concrete clients to the admin consumer-side ports, so neither
// internal/httpapi nor internal/admin imports the adapters (ARCHITECTURE §2).
type upstreamFactory struct {
	pool   *sql.DB
	logger *slog.Logger
}

// upstreamFactory implements admin.UpstreamFactory.
var _ admin.UpstreamFactory = (*upstreamFactory)(nil)

// ForConnection reads the connection and builds its Evolution and Chatwoot
// clients. Credentials never reach a log.
func (f *upstreamFactory) ForConnection(ctx context.Context, connectionID int) (admin.EvolutionClient, admin.ChatwootClient, error) {
	conn, err := mysql.NewAdminConnections(f.pool).Get(ctx, connectionID)
	if err != nil {
		return nil, nil, fmt.Errorf("read connection: %w", err)
	}
	if conn == nil {
		return nil, nil, fmt.Errorf("connection %d not found", connectionID)
	}
	cw, err := chatwoot.New(chatwoot.Config{
		APIURL:    conn.ChatwootAPIURL,
		APIToken:  conn.ChatwootAPIToken,
		AccountID: conn.ChatwootAccountID,
	}, chatwoot.WithLogger(f.logger))
	if err != nil {
		return nil, nil, fmt.Errorf("chatwoot client: %w", err)
	}
	evo, err := evolution.New(evolution.Config{
		APIURL:        conn.EvolutionAPIURL,
		GlobalAPIKey:  conn.EvolutionAPIKey,
		InstanceName:  conn.EvolutionInstanceName,
		InstanceToken: conn.EvolutionInstanceToken,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("evolution client: %w", err)
	}
	return evolutionAdapter{client: evo}, chatwootAdapter{client: cw}, nil
}

// ChatwootForCredentials builds a Chatwoot client from explicit credentials for
// the inbox-query endpoint.
func (f *upstreamFactory) ChatwootForCredentials(_ context.Context, apiURL, apiToken string, accountID int) (admin.ChatwootClient, error) {
	cw, err := chatwoot.New(chatwoot.Config{APIURL: apiURL, APIToken: apiToken, AccountID: accountID}, chatwoot.WithLogger(f.logger))
	if err != nil {
		return nil, err
	}
	return chatwootAdapter{client: cw}, nil
}

// evolutionAdapter adapts *evolution.Client to admin.EvolutionClient.
type evolutionAdapter struct {
	client *evolution.Client
}

func (a evolutionAdapter) ResolveInstanceToken(ctx context.Context) (string, error) {
	return a.client.ResolveInstanceToken(ctx)
}

func (a evolutionAdapter) GetInstanceStatus(ctx context.Context) (map[string]any, error) {
	return a.client.GetInstanceStatus(ctx)
}

func (a evolutionAdapter) FetchContacts(ctx context.Context) ([]map[string]any, error) {
	return a.client.FetchContacts(ctx)
}

func (a evolutionAdapter) SetWebhook(ctx context.Context, webhookURL string, subscribe []string) error {
	return a.client.SetWebhook(ctx, webhookURL, subscribe)
}

func (a evolutionAdapter) HealthCheck(ctx context.Context) error {
	return a.client.HealthCheck(ctx)
}

// chatwootAdapter adapts *chatwoot.Client to admin.ChatwootClient.
type chatwootAdapter struct {
	client *chatwoot.Client
}

func (a chatwootAdapter) ListInboxes(ctx context.Context) ([]admin.Inbox, error) {
	inboxes, err := a.client.ListInboxes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]admin.Inbox, 0, len(inboxes))
	for _, in := range inboxes {
		out = append(out, toAdminInbox(in))
	}
	return out, nil
}

func (a chatwootAdapter) GetInbox(ctx context.Context, inboxID int) (*admin.Inbox, error) {
	inbox, err := a.client.GetInbox(ctx, inboxID)
	if err != nil {
		return nil, err
	}
	converted := toAdminInbox(*inbox)
	return &converted, nil
}

func (a chatwootAdapter) CreateInbox(ctx context.Context, params admin.InboxParams) (*admin.Inbox, error) {
	inbox, err := a.client.CreateInbox(ctx, toChatwootInboxParams(params))
	if err != nil {
		return nil, err
	}
	converted := toAdminInbox(*inbox)
	return &converted, nil
}

func (a chatwootAdapter) UpdateInbox(ctx context.Context, inboxID int, params admin.InboxParams) (*admin.Inbox, error) {
	inbox, err := a.client.UpdateInbox(ctx, inboxID, toChatwootInboxParams(params))
	if err != nil {
		return nil, err
	}
	converted := toAdminInbox(*inbox)
	return &converted, nil
}

func (a chatwootAdapter) SearchContactExact(ctx context.Context, identity string, kind admin.ContactKind) (*admin.Contact, error) {
	contact, err := a.client.SearchContactExact(ctx, identity, chatwoot.Kind(kind))
	if err != nil || contact == nil {
		return nil, err
	}
	return &admin.Contact{
		ID:          contact.ID,
		Name:        contact.Name,
		PhoneNumber: contact.PhoneNumber,
		Identifier:  contact.Identifier,
	}, nil
}

func (a chatwootAdapter) CreateContact(ctx context.Context, params admin.ContactParams) (*admin.Contact, error) {
	contact, err := a.client.CreateContact(ctx, chatwoot.ContactParams{
		Kind:      chatwoot.Kind(params.Kind),
		Identity:  params.Identity,
		Name:      params.Name,
		AvatarURL: params.AvatarURL,
	})
	if err != nil {
		return nil, err
	}
	return &admin.Contact{
		ID:          contact.ID,
		Name:        contact.Name,
		PhoneNumber: contact.PhoneNumber,
		Identifier:  contact.Identifier,
	}, nil
}

func toAdminInbox(in chatwoot.Inbox) admin.Inbox {
	return admin.Inbox{ID: in.ID, Name: in.Name, ChannelType: in.ChannelType, WebhookURL: in.WebhookURL}
}

func toChatwootInboxParams(params admin.InboxParams) chatwoot.InboxParams {
	return chatwoot.InboxParams{
		Name:       params.Name,
		WebhookURL: params.WebhookURL,
		InboxSettings: chatwoot.InboxSettings{
			GreetingEnabled:            params.Settings.GreetingEnabled,
			GreetingMessage:            params.Settings.GreetingMessage,
			EnableAutoAssignment:       params.Settings.EnableAutoAssignment,
			CSATSurveyEnabled:          params.Settings.CSATSurveyEnabled,
			WorkingHoursEnabled:        params.Settings.WorkingHoursEnabled,
			OutOfOfficeMessage:         params.Settings.OutOfOfficeMessage,
			Timezone:                   params.Settings.Timezone,
			AllowMessagesAfterResolved: params.Settings.AllowMessagesAfterResolved,
			LockToSingleConversation:   params.Settings.LockToSingleConversation,
			SenderNameType:             params.Settings.SenderNameType,
			BusinessName:               params.Settings.BusinessName,
		},
	}
}

// buildAdminDeps wires the Phase 5 admin surface over the shared pool.
func buildAdminDeps(cfg config.Config, pool *sql.DB, logger *slog.Logger) *httpapi.AdminDeps {
	return &httpapi.AdminDeps{
		Auth:           auth.New(mysql.NewAdminUsers(pool), auth.Config{Secret: cfg.SessionSecret}),
		Connections:    mysql.NewAdminConnections(pool),
		Logs:           mysql.NewMessageLogsQuery(pool),
		Dashboard:      mysql.NewDashboard(pool),
		Settings:       mysql.NewBridgeSettings(pool),
		Notifications:  mysql.NewNotifications(pool),
		Upstream:       &upstreamFactory{pool: pool, logger: logger},
		BridgeURL:      cfg.BridgeURL,
		WebhookSecret:  cfg.WebhookTokenSecret,
		CookieSecure:   cfg.CookieSecure,
		CookieSameSite: cookieSameSite(cfg.CookieSameSite),
	}
}

// bootstrapAdmin creates the first admin from ADMIN_USER/ADMIN_PASS when the
// admin_users table is empty (ADR-0006 §2, legacy parity). It never logs the
// password.
func bootstrapAdmin(ctx context.Context, pool *sql.DB, cfg config.Config, logger *slog.Logger) error {
	if cfg.AdminUser == "" {
		return nil
	}
	store := mysql.NewAdminUsers(pool)
	count, err := store.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPass), auth.BcryptCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap admin password: %w", err)
	}
	if _, err := store.Create(ctx, cfg.AdminUser, string(hash), ""); err != nil {
		return err
	}
	logger.Info("admin user bootstrapped from environment", "username", cfg.AdminUser)
	return nil
}

// cookieSameSite maps the validated config value to net/http's SameSite.
func cookieSameSite(value string) http.SameSite {
	switch value {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
