/**
 * F2 — the backend never serves the SPA's assets (ADR-0007 §3).
 *
 * Mirrors backend test A17. This side performs the repo scan; the live handler
 * probe (`GET /`, `/index.html`, `/assets/*` → 404, not HTML) is owned by the
 * backend suite (`internal/httpapi`), which is the only place the Go server runs.
 *
 * The scan is intentionally tolerant while the backend admin API is still being
 * built: it asserts *absence*, so it passes today and fails the moment someone
 * embeds/serves the frontend from Go.
 */
import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { FRONTEND_DIR, REPO_ROOT, readFiles } from "./helpers";

// Production Go sources only: the backend's own A17 test *lists* these patterns
// as forbidden strings, so scanning test files would false-positive on it.
const BACKEND_GO_FILES = readFiles(
  path.join(REPO_ROOT, "backend"),
  name => name.endsWith(".go") && !name.endsWith("_test.go")
);

const SERVING_PATTERNS: Array<{ pattern: RegExp; label: string }> = [
  { pattern: /\bhttp\.FileServer\b/, label: "http.FileServer" },
  { pattern: /\bhttp\.Dir\b/, label: "http.Dir" },
  { pattern: /\bhttp\.ServeFile\b/, label: "http.ServeFile" },
  {
    pattern: /\/\/go:embed[^\n]*\b(frontend|client)\b/,
    label: "//go:embed of frontend/ or client/",
  },
];

describe("F2 backend serves no frontend assets", () => {
  for (const { pattern, label } of SERVING_PATTERNS) {
    it(`backend has no ${label}`, () => {
      const offenders = BACKEND_GO_FILES.filter(f => pattern.test(f.content)).map(
        f => path.relative(REPO_ROOT, f.path)
      );
      expect(offenders).toEqual([]);
    });
  }

  it("the frontend Dockerfile does not copy backend sources", () => {
    const dockerfile = fs.readFileSync(
      path.join(FRONTEND_DIR, "Dockerfile"),
      "utf-8"
    );
    expect(dockerfile).not.toMatch(/COPY\s+.*\bbackend\b/);
    expect(dockerfile).not.toMatch(/COPY\s+.*\.\./);
  });
});
