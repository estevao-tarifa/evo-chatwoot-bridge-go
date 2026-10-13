import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const FRONTEND_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  ".."
);
export const REPO_ROOT = path.resolve(FRONTEND_DIR, "..");

/** Read every file under `dir` (recursively) whose name matches `filter`. */
export function readFiles(
  dir: string,
  filter: (name: string) => boolean
): Array<{ path: string; content: string }> {
  if (!fs.existsSync(dir)) return [];
  return fs
    .readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter(entry => entry.isFile() && filter(entry.name))
    .map(entry => {
      const full = path.join(entry.parentPath, entry.name);
      return { path: full, content: fs.readFileSync(full, "utf-8") };
    });
}
