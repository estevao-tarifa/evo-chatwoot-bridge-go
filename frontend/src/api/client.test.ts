/**
 * F4 (401 → login redirect) and F5 (mutations send X-CSRF + credentials).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiClientError,
  LOGIN_PATH,
  http,
  setUnauthorizedHandler,
} from "./client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("REST client", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    setUnauthorizedHandler(null);
    vi.unstubAllGlobals();
  });

  describe("F4 401 redirects to login", () => {
    it("invokes the unauthorized handler on a 401 response", async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ error: { code: "unauthorized", message: "unauthorized" } }, 401)
      );
      const handler = vi.fn();
      setUnauthorizedHandler(handler);

      await expect(http.get("/api/v1/auth/me")).rejects.toBeInstanceOf(
        ApiClientError
      );
      expect(handler).toHaveBeenCalledTimes(1);
    });

    it("invokes the handler when the envelope code is unauthorized on a non-401 status", async () => {
      // Defensive: the spec names both HTTP 401 and error.code as triggers.
      fetchMock.mockResolvedValue(
        jsonResponse({ error: { code: "unauthorized", message: "no" } }, 403)
      );
      const handler = vi.fn();
      setUnauthorizedHandler(handler);
      await expect(http.get("/api/v1/logs")).rejects.toMatchObject({
        code: "unauthorized",
      });
      expect(handler).toHaveBeenCalled();
    });

    it("exposes the standalone login path", () => {
      expect(LOGIN_PATH).toBe("/");
    });
  });

  describe("F5 mutations send CSRF + credentials", () => {
    it("POST sends X-CSRF:1, credentials:include and JSON content type", async () => {
      fetchMock.mockResolvedValue(jsonResponse({ success: true }));
      await http.post("/api/v1/connections", { name: "synthetic" });

      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe("/api/v1/connections");
      expect(init.method).toBe("POST");
      expect(init.credentials).toBe("include");
      expect((init.headers as Record<string, string>)["X-CSRF"]).toBe("1");
      expect((init.headers as Record<string, string>)["Content-Type"]).toBe(
        "application/json"
      );
      expect(init.body).toBe(JSON.stringify({ name: "synthetic" }));
    });

    it("PATCH and DELETE also send X-CSRF:1", async () => {
      fetchMock.mockImplementation(() =>
        Promise.resolve(jsonResponse({ success: true }))
      );
      await http.patch("/api/v1/connections/1", { isActive: false });
      await http.delete("/api/v1/connections/1");

      for (const [, init] of fetchMock.mock.calls as Array<[string, RequestInit]>) {
        expect((init.headers as Record<string, string>)["X-CSRF"]).toBe("1");
        expect(init.credentials).toBe("include");
      }
    });

    it("GET does not send X-CSRF", async () => {
      fetchMock.mockResolvedValue(jsonResponse({ logs: [], total: 0 }));
      await http.get("/api/v1/logs", { query: { limit: 25, offset: 0 } });

      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe("/api/v1/logs?limit=25&offset=0");
      expect(init.method).toBe("GET");
      expect((init.headers as Record<string, string>)["X-CSRF"]).toBeUndefined();
    });
  });

  it("parses the error envelope into ApiClientError", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "invalid_request", message: "bad" } }, 400)
    );
    await expect(http.get("/api/v1/logs")).rejects.toMatchObject({
      code: "invalid_request",
      message: "bad",
      status: 400,
    });
  });
});
