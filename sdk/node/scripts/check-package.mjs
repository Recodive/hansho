// Builds the package and checks what would be published: only dist/, README.md and LICENSE
// (plus the package.json npm always adds), and that both module formats load.
import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import assert from "node:assert/strict";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const npm = process.platform === "win32" ? "npm.cmd" : "npm";

execFileSync(npm, ["run", "build"], { cwd: root, stdio: "inherit" });
const out = JSON.parse(execFileSync(npm, ["pack", "--dry-run", "--json", "--ignore-scripts"], { cwd: root, encoding: "utf8" }));
const files = out[0].files.map((f) => f.path).sort();
for (const f of files) {
  assert.ok(f === "package.json" || f === "README.md" || f === "LICENSE" || f.startsWith("dist/"), `unexpected file in package: ${f}`);
}
for (const f of ["dist/index.js", "dist/index.cjs", "dist/index.d.ts", "dist/index.d.cts", "README.md", "LICENSE"]) {
  assert.ok(files.includes(f), `missing ${f}`);
}

const esm = await import(pathToFileURL(join(root, "dist/index.js")).href);
const cjs = createRequire(import.meta.url)(join(root, "dist/index.cjs"));
for (const m of [esm, cjs]) {
  assert.equal(typeof m.Hansho, "function");
  assert.equal(typeof m.verifyWebhook, "function");
  assert.equal(m.NAME, "hansho");
}
console.log("package ok:", files.join(", "));
