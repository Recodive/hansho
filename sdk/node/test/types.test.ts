import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

const tsc = fileURLToPath(new URL("../node_modules/typescript/bin/tsc", import.meta.url));
const fixtures = fileURLToPath(new URL("./fixtures/", import.meta.url));

function run(args: string[]) {
  return spawnSync(process.execPath, [tsc, ...args], { encoding: "utf8" });
}

describe("compile-time guarantees", { skip: !existsSync(tsc) && "typescript is not installed" }, () => {
  it("accepts valid levels and rejects level: 'pager'", () => {
    // levels-ok.ts uses @ts-expect-error: it only compiles if "pager" IS an error.
    const r = run(["-p", `${fixtures}tsconfig.json`]);
    assert.equal(r.status, 0, r.stdout + r.stderr);
  });

  it("reports the unknown level as a type error", () => {
    const r = run(["--noEmit", "--strict", "--allowImportingTsExtensions", "--skipLibCheck", "--module", "esnext", "--moduleResolution", "bundler", "--target", "es2022", "--types", "node", "--typeRoots", fileURLToPath(new URL("../node_modules/@types", import.meta.url)), `${fixtures}levels-bad.ts`]);
    assert.notEqual(r.status, 0);
    assert.match(r.stdout, /TS2(322|820)/);
    assert.match(r.stdout, /"pager"/);
  });
});
