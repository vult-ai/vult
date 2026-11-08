import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { createRequire } from "node:module";
import { build } from "esbuild";
import { withDOM } from "./helpers/dom.mjs";

async function modules() {
  const bundle = await build({
    stdin: {
      contents: 'export { hydrateBookCatalog } from "./lib/book-routing"; export { rememberDashboardSelection, rememberedDashboardSelection } from "./lib/dashboard-location";',
      resolveDir: new URL("../src", import.meta.url).pathname,
      loader: "ts",
    },
    bundle: true, write: false, platform: "node", format: "cjs", packages: "external", plugins: [phosphorPlugin],
    loader: { ".css": "empty" },
  });
  const loaded = { exports: {} };
  new Function("module", "exports", "require", bundle.outputFiles[0].text)(loaded, loaded.exports, createRequire(import.meta.url));
  return loaded.exports;
}
const book = { id: "11111111-1111-4111-8111-111111111111", name: "Example Studio", currency: "USD", scale: 2, start_date: "2025-01-01" };
const selection = { period: "custom", range: { from: "2026-01-01", to: "2026-03-31" } };
const settle = () => new Promise((done) => setTimeout(done, 20));

test("a chosen Dashboard period is saved for the owner when the catalog names their scope", async () => {
  await withDOM(async ({ window }) => {
    const saves = [];
    const previous = globalThis.fetch;
    globalThis.fetch = async (url, init = {}) => {
      const path = new URL(String(url), window.location.href).pathname;
      if (path === "/auth/session") return Response.json({ mode: "local", user_id: "a".repeat(64), csrf: "token" });
      if (path === "/api/v1/book-view-preference") {
        saves.push(JSON.parse(init.body));
        return Response.json({ schema_version: 1, status: "saved" });
      }
      return Response.json({}, { status: 404 });
    };
    try {
      const { hydrateBookCatalog, rememberDashboardSelection, rememberedDashboardSelection } = await modules();
      hydrateBookCatalog({ books: [book], addresses: [], view_scope: "owner-scope", views: {} });
      rememberDashboardSelection(book, selection);
      await settle();
      assert.deepEqual(saves, [{ book_id: book.id, scope: "owner-scope", selection }]);
      assert.deepEqual(rememberedDashboardSelection(book.id), selection);
      // Another session's saved choice arrives with the catalog and wins over nothing local.
      hydrateBookCatalog({ books: [book], addresses: [], view_scope: "owner-scope", views: { [book.id]: { period: "custom", range: { from: "2025-01-01", to: "2025-12-31" } } } });
      window.localStorage.clear();
      assert.deepEqual(rememberedDashboardSelection(book.id).range, { from: "2025-01-01", to: "2025-12-31" });
    } finally {
      globalThis.fetch = previous;
    }
  });
});

test("without an owner scope the period stays in this browser and nothing is sent", async () => {
  await withDOM(async () => {
    let sent = 0;
    const previous = globalThis.fetch;
    globalThis.fetch = async () => { sent++; return Response.json({}); };
    try {
      const { hydrateBookCatalog, rememberDashboardSelection, rememberedDashboardSelection } = await modules();
      hydrateBookCatalog({ books: [book], addresses: [] });
      rememberDashboardSelection(book, selection);
      await settle();
      assert.equal(sent, 0);
      assert.deepEqual(rememberedDashboardSelection(book.id), selection);
    } finally {
      globalThis.fetch = previous;
    }
  });
});
