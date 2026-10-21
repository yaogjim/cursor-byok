import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { staticI18nPlugin, syncCatalogFiles } from "./static-i18n-plugin.js";

test("translation catalog and transform exclude test fixtures", () => {
  const root = mkdtempSync(path.join(tmpdir(), "byok-i18n-test-"));
  try {
    mkdirSync(path.join(root, "src"));
    writeFileSync(path.join(root, "src/account.js"), 'export const label = "当前激活";');
    for (const filename of ["account.test.js", "account.spec.ts"]) {
      writeFileSync(path.join(root, "src", filename), 'const fixture = "测试专用账号";');
    }
    syncCatalogFiles(root);
    const catalog = JSON.parse(readFileSync(path.join(root, "src/i18n/generated/catalog.json"), "utf8"));
    assert.deepEqual(Object.values(catalog.entries).map((entry) => entry.source), ["当前激活"]);
    const plugin = staticI18nPlugin();
    plugin.configResolved({ root });
    assert.equal(plugin.transform('const fixture = "测试专用账号";', path.join(root, "src/account.test.js")), null);
    assert.notEqual(plugin.transform('export const label = "当前激活";', path.join(root, "src/account.js")), null);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});