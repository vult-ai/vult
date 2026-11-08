import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Example Studio", currency: "USD", scale: 2, start_date: "2025-01-01" };
const settle = () => React.act(() => new Promise((done) => setTimeout(done, 30)));

test("a local installation lists its durable agent connections", async () => {
  await withDOM(async ({ render }) => {
    const previous = globalThis.fetch;
    globalThis.fetch = async (url) => {
      const path = new URL(String(url), "http://localhost").pathname;
      if (path === "/auth/session") return Response.json({ mode: "local", user_id: "a".repeat(64), csrf: "token" });
      if (path === "/api/v1/books") return Response.json({ books: [book] });
      if (path === "/api/v1/access/tokens")
        return Response.json({ schema_version: 1, tokens: [
          { id: "22222222-2222-4222-8222-222222222222", name: "Studio agent", connection: true, created_at: "2026-11-01T10:00:00Z", expires_at: null, book_grants: [{ book_id: book.id, role: "manage", rights: ["read", "write"] }] },
          { id: "33333333-3333-4333-8333-333333333333", name: "Old agent", connection: true, created_at: "2026-10-01T10:00:00Z", expires_at: null, book_grants: [{ book_id: book.id, role: "none", rights: [] }] },
        ] });
      return Response.json({}, { status: 404 });
    };
    try {
      const { AccessSettings } = await loadComponent("src/components/access-settings.tsx");
      await render(React.createElement(AccessSettings, { book }));
      for (let i = 0; i < 10 && !document.body.textContent.includes("Studio agent"); i++) await settle();
      assert.match(document.body.textContent, /Studio agent/);
      assert.ok(document.querySelector('button[aria-label^="Disconnect Studio agent"]'));
      assert.doesNotMatch(document.body.textContent, /Expires/, "a durable grant shows no expiry");
    } finally {
      globalThis.fetch = previous;
    }
  });
});
