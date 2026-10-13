/**
 * Response-shape contract (B1/B2).
 *
 * The backend speaks the shapes corrected in spec §1.2: the list is wrapped
 * (`{connections:[…]}`) and the reveal endpoints return `{webhookUrl}`.
 * `routes-contract.test.ts` only checks paths, so these assertions are the
 * ones that actually catch a response-shape regression.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./index";
import { ApiClientError } from "./client";
import type { Connection } from "./types";

const CONNECTION = { id: 1, name: "Synthetic" } as unknown as Connection;

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("response shapes match the documented backend contract", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("connections.list unwraps {connections:[…]} into a plain array", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ connections: [CONNECTION] }));

    const list = await api.connections.list();

    expect(Array.isArray(list)).toBe(true);
    expect(list).toHaveLength(1);
    expect(list[0]).toEqual(CONNECTION);
  });

  it("connections.list treats a bare array as a contract violation", async () => {
    // The old (wrong) type assumed a bare array. A bare array must NOT
    // masquerade as a valid list.
    fetchMock.mockResolvedValue(jsonResponse([CONNECTION]));

    await expect(api.connections.list()).rejects.toBeInstanceOf(ApiClientError);
  });

  it("reveal returns {webhookUrl}; the old {url} shape would render blank", async () => {
    const url = "https://bridge.example.test/api/webhooks/evolution/1/tok";
    fetchMock.mockResolvedValue(jsonResponse({ webhookUrl: url }));

    const reveal = await api.connections.revealEvolutionWebhookUrl(1);
    expect(reveal.webhookUrl).toBe(url);

    // The exact bug: reading `.webhookUrl` off a `{url}` body is undefined,
    // which is why the dialog was always blank.
    fetchMock.mockResolvedValue(jsonResponse({ url }));
    const wrong = await api.connections.revealEvolutionWebhookUrl(1);
    expect(wrong.webhookUrl).toBeUndefined();
  });
});
