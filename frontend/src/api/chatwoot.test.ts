/**
 * Inbox DTO shape contract (spec §1.2 corrected / §6.5 R2).
 *
 * Every inbox response (inboxes/query, inboxes, inbox-settings) exposes the
 * masked `webhookUrlMasked`; the raw token-bearing `webhookUrl` is only ever
 * returned by the two `/webhook-url` reveal endpoints.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./index";

const MASKED = "https://bridge.example.test/api/webhooks/chatwoot/1/***";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("inbox responses expose webhookUrlMasked, never the raw URL", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("listInboxes carries webhookUrlMasked and no raw webhookUrl", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse([{ id: 1, name: "Inbox", channelType: "Channel::Api", webhookUrlMasked: MASKED }])
    );

    const [inbox] = await api.chatwoot.listInboxes({ connectionId: 1 });

    expect(inbox.webhookUrlMasked).toBe(MASKED);
    expect(inbox).not.toHaveProperty("webhookUrl");
  });

  it("createInbox returns webhookUrlMasked", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ id: 2, name: "New", channelType: "Channel::Api", webhookUrlMasked: MASKED }, 201)
    );

    const inbox = await api.chatwoot.createInbox({ inboxName: "New" });

    expect(inbox.webhookUrlMasked).toBe(MASKED);
  });

  it("updateInboxSettings returns a masked inbox", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ success: true, inbox: { id: 3, name: "Inbox", webhookUrlMasked: MASKED } })
    );

    const result = await api.chatwoot.updateInboxSettings(1, { name: "Inbox" });

    expect(result.inbox.webhookUrlMasked).toBe(MASKED);
    expect(result.inbox).not.toHaveProperty("webhookUrl");
  });
});
