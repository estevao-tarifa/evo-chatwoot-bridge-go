/**
 * F7 — no tRPC / superjson / backend-module coupling remains.
 *
 * The legacy `client/src/lib/trpc.ts` imported `AppRouter` from `../../../server`,
 * which broke independent deployment (audit risks.md R1). This test is the
 * structural guard that it never comes back.
 */
import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { FRONTEND_DIR, readFiles } from "./helpers";

const SOURCE_FILES = readFiles(
  path.join(FRONTEND_DIR, "src"),
  name => /\.(ts|tsx)$/.test(name)
);

const FORBIDDEN: Array<{ pattern: RegExp; label: string }> = [
  { pattern: /(?:from|require\()\s*["']@trpc\//, label: "@trpc/* import" },
  { pattern: /(?:from|require\()\s*["']superjson["']/, label: "superjson import" },
  { pattern: /\bAppRouter\b/, label: "AppRouter (backend module import)" },
  { pattern: /["']\.\.\/\.\.\/server/, label: "relative import of the backend server/" },
];

describe("F7 no tRPC remains", () => {
  it("scans a non-trivial number of source files", () => {
    expect(SOURCE_FILES.length).toBeGreaterThan(10);
  });

  for (const { pattern, label } of FORBIDDEN) {
    it(`src has no reference to ${label}`, () => {
      const offenders = SOURCE_FILES.filter(f => pattern.test(f.content)).map(
        f => path.relative(FRONTEND_DIR, f.path)
      );
      expect(offenders).toEqual([]);
    });
  }

  it("package.json has no tRPC / superjson / Manus dependency", () => {
    const pkg = JSON.parse(
      fs.readFileSync(path.join(FRONTEND_DIR, "package.json"), "utf-8")
    );
    const deps = Object.keys({
      ...(pkg.dependencies ?? {}),
      ...(pkg.devDependencies ?? {}),
    });
    const banned = deps.filter(d =>
      /^@trpc\/|^superjson$|^vite-plugin-manus-runtime$|^@builder\.io\/|^@aws-sdk\/|^streamdown$|^framer-motion$|^axios$|^express$|^drizzle-orm$|^mysql2$|^bcryptjs$|^jose$|^nodemailer$|^zod$|^sharp$/.test(
        d
      )
    );
    expect(banned).toEqual([]);
  });
});
