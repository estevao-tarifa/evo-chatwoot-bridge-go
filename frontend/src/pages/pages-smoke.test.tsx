/**
 * F8 — Home / Connections / ConnectionForm / Logs / Notifications render
 * against a mocked REST client.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { Route, Router } from "wouter";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Connection } from "@/api";

const mocks = vi.hoisted(() => ({
  stats: vi.fn(),
  perHour: vi.fn(),
  perDay: vi.fn(),
  types: vi.fn(),
  connGet: vi.fn(),
  webhookStatus: vi.fn(),
  inboxes: vi.fn(),
  logsList: vi.fn(),
  notifSettings: vi.fn(),
  notifAlerts: vi.fn(),
  notifFailure: vi.fn(),
  bridgeUrl: vi.fn(),
}));

// The real `connections.list` is kept so the `{connections:[…]}` wrapper is
// exercised end-to-end against a mocked fetch — a mocked array can no longer
// hide the contract (B3).
vi.mock("@/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api")>();
  return {
    ApiClientError: actual.ApiClientError,
    api: {
      auth: { state: vi.fn(), me: vi.fn(), login: vi.fn(), setup: vi.fn(), logout: vi.fn() },
      dashboard: {
        stats: mocks.stats,
        messagesPerHour: mocks.perHour,
        messagesPerDay: mocks.perDay,
        messageTypes: mocks.types,
      },
      connections: {
        list: actual.api.connections.list,
        getById: mocks.connGet,
        webhookStatus: mocks.webhookStatus,
        test: vi.fn(),
        update: vi.fn(),
        remove: vi.fn(),
        resolveToken: vi.fn(),
        configureWebhook: vi.fn(),
        revealEvolutionWebhookUrl: vi.fn(),
        revealChatwootWebhookUrl: vi.fn(),
      },
      chatwoot: {
        listInboxes: mocks.inboxes,
        createInbox: vi.fn(),
        updateInboxSettings: vi.fn(),
        configureWebhook: vi.fn(),
      },
      contacts: { sync: vi.fn() },
      notifications: {
        getSettings: mocks.notifSettings,
        updateSettings: vi.fn(),
        listAlerts: mocks.notifAlerts,
        failureStates: mocks.notifFailure,
      },
      logs: { list: mocks.logsList },
      settings: { getBridgeUrl: mocks.bridgeUrl },
    },
  };
});

const fetchMock = vi.fn();

import Home from "./Home";
import Connections from "./Connections";
import ConnectionForm from "./ConnectionForm";
import Logs from "./Logs";
import Notifications from "./Notifications";

const CONNECTION: Connection = {
  id: 1,
  name: "Synthetic Connection",
  evolutionApiUrl: "https://evo.example.test",
  hasEvolutionApiKey: true,
  evolutionInstanceName: "synthetic-instance",
  hasEvolutionInstanceToken: true,
  chatwootApiUrl: "https://chat.example.test",
  hasChatwootApiToken: true,
  chatwootAccountId: 1,
  chatwootInboxId: 2,
  chatwootInboxName: "Synthetic Inbox",
  isActive: true,
  ignoreGroups: true,
  signMessages: false,
  autoReadMessages: false,
  rejectCalls: false,
  rejectCallMessage: null,
  n8nWebhookUrl: null,
  webhookConfigured: true,
  inboxGreetingEnabled: false,
  inboxGreetingMessage: null,
  inboxAutoAssignment: true,
  inboxCsatEnabled: false,
  inboxWorkingHoursEnabled: false,
  inboxOutOfOfficeMessage: null,
  inboxTimezone: "America/Sao_Paulo",
  inboxAllowAfterResolved: true,
  inboxLockSingleConversation: false,
  inboxSenderNameType: "friendly",
  inboxBusinessName: null,
  createdAt: "2026-10-12T00:00:00Z",
  updatedAt: "2026-10-12T00:00:00Z",
} as unknown as Connection;

function renderAt(path: string, ui: React.ReactElement) {
  window.history.pushState({}, "", path);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <Router>{ui}</Router>
    </QueryClientProvider>
  );
}

/** Renders a page behind a wouter route so `useParams` resolves. */
function renderRoute(path: string, pattern: string, page: React.ComponentType) {
  window.history.pushState({}, "", path);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <Router>
        <Route path={pattern} component={page} />
      </Router>
    </QueryClientProvider>
  );
}

function primeMocks() {
  mocks.stats.mockResolvedValue({
    totalConnections: 1,
    activeConnections: 1,
    totalMessages: 10,
    failedMessages: 0,
    todayMessages: 5,
    recentLogs: [],
  });
  mocks.perHour.mockResolvedValue([]);
  mocks.perDay.mockResolvedValue([]);
  mocks.types.mockResolvedValue([]);
  // Real wrapper shape from the backend; the real client unwraps it.
  fetchMock.mockResolvedValue(
    new Response(JSON.stringify({ connections: [CONNECTION] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  );
  mocks.connGet.mockResolvedValue(CONNECTION);
  mocks.webhookStatus.mockResolvedValue({
    evolution: { configured: true, urlMasked: "https://bridge.example.test/api/webhooks/evolution/1/***" },
    chatwoot: { configured: true, urlMasked: "https://bridge.example.test/api/webhooks/chatwoot/1/***" },
    instanceStatus: "connected",
  });
  mocks.inboxes.mockResolvedValue([]);
  mocks.logsList.mockResolvedValue({ logs: [], total: 0 });
  mocks.notifSettings.mockResolvedValue({
    enabled: false,
    failureThreshold: 3,
    cooldownMinutes: 30,
    whatsappEnabled: false,
    whatsappConnectionId: null,
    whatsappAlertPhone: null,
    emailEnabled: false,
    smtpHost: null,
    smtpPort: 587,
    smtpUser: null,
    hasSmtpPass: false,
    smtpSecure: false,
    emailFrom: null,
    emailTo: null,
  });
  mocks.notifAlerts.mockResolvedValue({ alerts: [], total: 0 });
  mocks.notifFailure.mockResolvedValue({});
  mocks.bridgeUrl.mockResolvedValue({ url: null, source: "none" });
}

describe("F8 pages smoke", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("Home renders dashboard stats", async () => {
    primeMocks();
    renderAt("/", <Home />);
    expect(await screen.findByText("Dashboard")).toBeInTheDocument();
    expect(await screen.findByText("Conexões Ativas")).toBeInTheDocument();
  });

  it("Connections renders the list", async () => {
    primeMocks();
    renderAt("/connections", <Connections />);
    expect(await screen.findByText("Synthetic Connection")).toBeInTheDocument();
  });

  it("ConnectionForm renders the edit form", async () => {
    primeMocks();
    renderRoute("/connections/1/edit", "/connections/:id/edit", ConnectionForm);
    expect(await screen.findByText("Editar Conexão")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("Synthetic Connection")).toBeInTheDocument();
  });

  it("Logs renders", async () => {
    primeMocks();
    renderAt("/logs", <Logs />);
    expect(await screen.findByText("Logs de Mensagens")).toBeInTheDocument();
  });

  it("Notifications renders", async () => {
    primeMocks();
    renderAt("/notifications", <Notifications />);
    expect(await screen.findByText("Notificações")).toBeInTheDocument();
    expect(await screen.findByText("Configurações Gerais")).toBeInTheDocument();
  });
});
