/**
 * F9 (local half) — the frontend builds and serves itself.
 *
 * The real `docker build` + container probe runs in CI (`.github/workflows/
 * frontend.yml`). Here we assert the deployment contract on disk: static nginx
 * image, SPA fallback, runtime `config.js`, and no backend coupling.
 */
import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { FRONTEND_DIR } from "./helpers";

const read = (name: string) =>
  fs.readFileSync(path.join(FRONTEND_DIR, name), "utf-8");

describe("F9 frontend deployment artifacts", () => {
  it("Dockerfile builds with npm and serves with nginx", () => {
    const dockerfile = read("Dockerfile");
    expect(dockerfile).toMatch(/npm ci/);
    expect(dockerfile).toMatch(/npm run build/);
    expect(dockerfile).toMatch(/FROM nginx:/);
    expect(dockerfile).toMatch(/COPY --from=build .*dist/);
  });

  it("nginx.conf has an SPA fallback and serves config.js uncached", () => {
    const nginx = read("nginx.conf");
    expect(nginx).toMatch(/try_files\s+\$uri\s+\$uri\/\s+\/index\.html/);
    expect(nginx).toMatch(/location = \/config\.js/);
    expect(nginx).toMatch(/no-store/);
  });

  it("index.html loads /config.js and config.js defines the runtime base URL", () => {
    expect(read("index.html")).toMatch(/src="\/config\.js"/);
    expect(read("public/config.js")).toMatch(/__BRIDGE_CONFIG__/);
    expect(read("public/config.js")).toMatch(/apiBaseUrl/);
  });

  it("the entrypoint writes config.js from BRIDGE_API_BASE_URL", () => {
    const entrypoint = read("docker-entrypoint.sh");
    expect(entrypoint).toMatch(/BRIDGE_API_BASE_URL/);
    expect(entrypoint).toMatch(/__BRIDGE_CONFIG__/);
  });

  it("build output is static (no backend import in package.json)", () => {
    const pkg = JSON.parse(read("package.json"));
    const deps = Object.keys({
      ...(pkg.dependencies ?? {}),
      ...(pkg.devDependencies ?? {}),
    });
    expect(deps.some(d => d === "express" || d.startsWith("@trpc/"))).toBe(false);
    expect(pkg.scripts.build).toMatch(/vite build/);
  });
});
