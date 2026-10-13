/**
 * F3 — every REST path the client calls exists in the contract.
 *
 * Source of truth: the canonical `backend/api/openapi.yaml`, cross-checked
 * against `frontend/src/api/routes.ts`, the SPA's single source of truth for
 * paths. Forward check: every route template the SPA calls is declared in the
 * OpenAPI document. Backward check: every `/api/v1` path in that document is
 * either consumed by the SPA or explicitly listed as non-SPA (ops/meta).
 */
import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { routeTemplates } from "@/api/routes";
import { REPO_ROOT } from "./helpers";

const OPENAPI_PATH = path.join(REPO_ROOT, "backend/api/openapi.yaml");

const templates = Object.entries(routeTemplates) as Array<[string, string]>;

/** Extract top-level `paths:` keys from an OpenAPI document. */
function extractOpenApiPaths(yaml: string): Set<string> {
  const found = new Set<string>();
  for (const line of yaml.split("\n")) {
    const match = line.match(/^ {2}(\/api\/v1\/\S+):\s*$/);
    if (match) found.add(match[1]);
  }
  return found;
}

const openapiPaths = extractOpenApiPaths(
  fs.readFileSync(OPENAPI_PATH, "utf-8")
);

/** Collapse `{id}` / `{connectionId}` so param naming differences don't matter. */
function normalizeParams(p: string): string {
  return p.replace(/\{[^}]+\}/g, "{}");
}

const normalizedOpenApiPaths = new Set([...openapiPaths].map(normalizeParams));

// Public paths the SPA legitimately does not call (ops/meta).
const NON_SPA_PATHS: Record<string, true> = { "/api/v1/meta": true };

describe("F3 REST client paths match the contract", () => {
  it("found paths in openapi.yaml", () => {
    expect(openapiPaths.size).toBeGreaterThan(15);
  });

  for (const [name, template] of templates) {
    it(`routeTemplates.${name} (${template}) is in openapi.yaml`, () => {
      expect(normalizedOpenApiPaths.has(normalizeParams(template))).toBe(true);
    });
  }

  it("every client path is in openapi.yaml", () => {
    const missing = templates
      .map(([, template]) => template)
      .filter(template => !normalizedOpenApiPaths.has(normalizeParams(template)));
    expect(missing).toEqual([]);
  });

  it(
    "every /api/v1 path in openapi.yaml is consumed (or documented as non-SPA)",
    () => {
      const consumed = new Set(templates.map(([, template]) => normalizeParams(template)));
      const unconsumed = [...openapiPaths].filter(
        p => !consumed.has(normalizeParams(p)) && !NON_SPA_PATHS[p]
      );
      expect(unconsumed).toEqual([]);
    }
  );
});
