import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

/* Settings → Access in v0.4: the owner sees each connected agent and the Books it
   reaches, and can disconnect it. Connecting an agent and changing its Books are
   done in the terminal, so the browser offers neither. */
const book = { id: "11111111-1111-4111-8111-111111111111", name: "Selected Business", profile: "business", currency: "USD", scale: 2, start_date: "2026-01-01" };
const other = { ...book, id: "22222222-2222-4222-8222-222222222222", name: "Example Studio" };
const response = (data, ok = true, status = 200) => ({ ok, status, json: async () => data });
const button = (text, scope = document) => [...scope.querySelectorAll("button")].find((n) => n.textContent.trim() === text);
const click = async (node) => { assert.ok(node, "the action is reachable"); await React.act(async () => node.click()); };
const dialog = () => document.querySelector('[role="dialog"][data-state="open"]');

function server(entries, { failRevoke = false } = {}) {
  const sent = [];
  const fetch = async (url, init) => {
    if (url.endsWith("/session")) return response({ mode: "protected-single", csrf: "fixture" });
    if (url.endsWith("/books")) return response({ books: [book, other] });
    if (/\/access\/connection-request|\/access\/tokens\/[^/]+\/books/.test(url)) throw new Error(`the browser no longer calls ${url}`);
    if (init?.body) {
      sent.push(url);
      if (failRevoke) return response({ error: "unavailable" }, false, 503);
      const id = /\/access\/tokens\/([^/]+)\/revoke$/.exec(url)?.[1];
      entries.splice(entries.findIndex((e) => e.id === id), 1);
      return response({});
    }
    return response({ tokens: entries });
  };
  return { fetch, sent };
}
const agent = (id, grants) => ({ id, name: "Bookkeeping agent", connection: true, created_at: "2026-11-01T10:00:00Z", last_renewed_at: "2026-11-02T10:00:00Z", expires_at: "9999-12-31T00:00:00Z", book_grants: grants });

test("connected agents show their Books read-only and no browser setup", async () => {
  await withDOM(async ({ render, unmount }) => {
    const { AccessSettings } = await loadComponent("src/components/access-settings.tsx");
    const before = globalThis.fetch;
    const entries = [agent("a-1", [{ book_id: book.id, role: "manage", rights: [] }, { book_id: other.id, role: "view", rights: [] }])];
    globalThis.fetch = server(entries).fetch;
    try {
      await render(React.createElement(AccessSettings, { book }));
      const text = document.body.textContent;
      assert.match(text, /Bookkeeping agent/);
      assert.match(text, /Selected Business.*Manage records/);
      assert.match(text, /Example Studio.*View only/);
      assert.match(text, /vult connection create/, "the terminal is named as the way to connect");
      assert.equal(button("Connect an app"), undefined);
      assert.equal(document.querySelectorAll("select").length, 0, "Book access can't be edited here");
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("disconnect revokes exactly the chosen connection, and a failure keeps the dialog for retry", async () => {
  await withDOM(async ({ render, unmount }) => {
    const { AccessSettings } = await loadComponent("src/components/access-settings.tsx");
    const before = globalThis.fetch;
    const entries = [agent("a-1", [{ book_id: book.id, role: "manage", rights: [] }]), agent("a-2", [{ book_id: book.id, role: "view", rights: [] }])];
    const failing = server(entries, { failRevoke: true });
    globalThis.fetch = failing.fetch;
    try {
      await render(React.createElement(AccessSettings, { book }));
      await click(button("2 connections"));
      await click(document.querySelector('button[aria-label^="Disconnect Bookkeeping agent connection a-2"]'));
      await click(button("Disconnect", dialog()));
      assert.match(failing.sent[0], /\/access\/tokens\/a-2\/revoke$/);
      assert.ok(dialog(), "the dialog stays open after a failure");
      const working = server(entries);
      globalThis.fetch = working.fetch;
      await click(button("Disconnect", dialog()));
      assert.match(working.sent[0], /\/access\/tokens\/a-2\/revoke$/);
      assert.equal(entries.length, 1);
      assert.equal(entries[0].id, "a-1", "the other connection keeps its access");
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});
