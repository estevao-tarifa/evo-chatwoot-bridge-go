/**
 * Typed `fetch` wrapper for the bridge admin API.
 *
 * Conventions enforced here (spec §1.1):
 * - base URL from `window.__BRIDGE_CONFIG__.apiBaseUrl` (empty = same-origin);
 * - `credentials: "include"` (session cookie);
 * - JSON content type;
 * - `X-CSRF: 1` on POST/PATCH/DELETE;
 * - error envelope `{error:{code,message}}` parsed into `ApiClientError`;
 * - HTTP 401 (or `code === "unauthorized"`) triggers the login redirect.
 */
import type { ApiErrorBody } from "./types";

/** Where an unauthenticated request sends the user. The standalone auth gate
 * renders the login screen on the app root. */
export const LOGIN_PATH = "/";

export class ApiClientError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "ApiClientError";
    this.code = code;
    this.status = status;
  }

  get isUnauthorized(): boolean {
    return this.status === 401 || this.code === "unauthorized";
  }
}

interface BridgeConfig {
  apiBaseUrl?: string;
}

function bridgeConfig(): BridgeConfig {
  const cfg = (globalThis as { __BRIDGE_CONFIG__?: BridgeConfig })
    .__BRIDGE_CONFIG__;
  return cfg ?? {};
}

/** Absolute base URL without trailing slash; "" means same-origin. */
export function apiBaseUrl(): string {
  const base = bridgeConfig().apiBaseUrl;
  return typeof base === "string" ? base.replace(/\/+$/, "") : "";
}

/** Build the absolute URL for a path, keeping query strings intact. */
export function apiUrl(pathname: string): string {
  return `${apiBaseUrl()}${pathname}`;
}

type UnauthorizedHandler = () => void;

const defaultUnauthorizedHandler: UnauthorizedHandler = () => {
  if (typeof window !== "undefined") {
    window.location.href = LOGIN_PATH;
  }
};

let unauthorizedHandler: UnauthorizedHandler = defaultUnauthorizedHandler;

/** Override the 401 behaviour (used by tests). Pass `null` to restore default. */
export function setUnauthorizedHandler(
  handler: UnauthorizedHandler | null
): void {
  unauthorizedHandler = handler ?? defaultUnauthorizedHandler;
}

export function redirectToLogin(): void {
  unauthorizedHandler();
}

export type QueryValue = string | number | boolean | undefined | null;

export interface RequestOptions {
  query?: Record<string, QueryValue>;
  body?: unknown;
  signal?: AbortSignal;
}

const MUTATING_METHODS = new Set(["POST", "PATCH", "PUT", "DELETE"]);

function buildQuery(query?: Record<string, QueryValue>): string {
  if (!query) return "";
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

async function parseError(response: Response): Promise<ApiClientError> {
  let code = "internal";
  let message = `Request failed (${response.status})`;
  try {
    const body = (await response.json()) as Partial<ApiErrorBody>;
    if (body?.error?.code) code = body.error.code;
    if (body?.error?.message) message = body.error.message;
  } catch {
    /* non-JSON body: keep the generic message */
  }
  return new ApiClientError(code, message, response.status);
}

export async function request<T>(
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
  pathname: string,
  options: RequestOptions = {}
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const init: RequestInit = {
    method,
    credentials: "include",
    headers,
    signal: options.signal,
  };

  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }
  if (MUTATING_METHODS.has(method)) {
    headers["X-CSRF"] = "1";
  }

  let response: Response;
  try {
    response = await fetch(`${apiUrl(pathname)}${buildQuery(options.query)}`, init);
  } catch (error) {
    throw new ApiClientError(
      "network_error",
      error instanceof Error ? error.message : "Network error",
      0
    );
  }

  if (!response.ok) {
    const apiError = await parseError(response);
    if (apiError.isUnauthorized) {
      redirectToLogin();
    }
    throw apiError;
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const http = {
  get: <T>(pathname: string, options?: RequestOptions) =>
    request<T>("GET", pathname, options),
  post: <T>(pathname: string, body?: unknown, options?: RequestOptions) =>
    request<T>("POST", pathname, { ...options, body }),
  patch: <T>(pathname: string, body?: unknown, options?: RequestOptions) =>
    request<T>("PATCH", pathname, { ...options, body }),
  delete: <T>(pathname: string, options?: RequestOptions) =>
    request<T>("DELETE", pathname, options),
};
