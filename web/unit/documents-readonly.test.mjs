import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Synthetic Book", currency: "USD", scale: 2 };
const doc = (n, extra = {}) => ({ id: `doc-${n}`, sequence: n, name: `receipt-${n}.pdf`, media_type: "application/pdf", size: 2048 * n, sha256: "a".repeat(64), state: "ready", created_at: "2026-10-0" + n + "T10:00:00Z", ...extra });
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const settle = () => React.act(() => new Promise((r) => setTimeout(r, 10)));

test("Sources lists this Book's originals, pages with the cursor, and downloads rather than renders", async () => {
  await withDOM(async ({ render }) => {
    const { Documents } = await loadComponent("src/components/book/documents.tsx");
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url, init) => {
      const u = new URL(String(url), "http://localhost");
      calls.push({ path: u.pathname, query: u.searchParams, method: init?.method || "GET" });
      if (u.pathname === "/api/v1/documents") return json(u.searchParams.get("after") ? { documents: [doc(3)], next_after: 3, has_more: false } : { documents: [doc(1, { used_at: "2026-10-02T00:00:00Z" }), doc(2)], next_after: 2, has_more: true });
      if (u.pathname.endsWith("/entries")) return json({ uses: [{ entry_id: "entry-1", accounting_date: "2026-10-02", description: "Office chair", evidence: { role: "receipt" } }], next_after: 1, has_more: false });
      return json({ error: "not found" }, 404);
    };
    try {
      const opened = [];
      await render(React.createElement(Documents, { book, onOpenEntry: (id) => opened.push(id) }));
      await settle();
      const root = document.getElementById("root");
      assert.match(root.textContent, /receipt-1\.pdf/);
      assert.match(root.textContent, /In use/);
      assert.match(root.textContent, /Not yet used/);
      assert.equal(calls[0].query.get("book_id"), book.id);
      assert.ok(!/Rename|Archive|Delete/i.test(root.textContent), "originals are kept as added: no rename, archive or delete");
      // The table fills its first page from the next cursor page on its own; no "Load more".
      await settle();
      assert.ok(![...root.querySelectorAll("button")].some((b) => b.textContent === "Load more"));
      assert.match(root.textContent, /receipt-3\.pdf/);
      assert.equal(calls.at(-1).query.get("after"), "2");
      await React.act(async () => root.querySelector('button[aria-label="Inspect receipt-1.pdf"]').click());
      await settle();
      const download = [...root.querySelectorAll("a")].find((a) => a.textContent.includes("Download original"));
      assert.match(download.getAttribute("href"), /\/api\/v1\/documents\/doc-1\/content\?book_id=/);
      assert.equal(download.getAttribute("target"), null, "never opened inside Vult");
      assert.match(root.textContent, /Office chair/);
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent.includes("Office chair")).click());
      assert.deepEqual(opened, ["entry-1"]);
      assert.ok(calls.every((c) => c.method === "GET"));
    } finally { globalThis.fetch = original; }
  });
});

test("Sources hides Used by when the server does not offer it, and keeps the list on a failed page", async () => {
  await withDOM(async ({ render }) => {
    const { Documents } = await loadComponent("src/components/book/documents.tsx");
    const original = globalThis.fetch;
    globalThis.fetch = async (url) => {
      const u = new URL(String(url), "http://localhost");
      if (u.pathname === "/api/v1/documents") return json({ documents: [doc(1)], next_after: 1, has_more: false });
      return json({ error: "not found" }, 404);
    };
    try {
      await render(React.createElement(Documents, { book }));
      await settle();
      const root = document.getElementById("root");
      await React.act(async () => root.querySelector('button[aria-label="Inspect receipt-1.pdf"]').click());
      await settle();
      assert.ok(!/Used by/.test(root.textContent));
      assert.match(root.textContent, /Download original/);
    } finally { globalThis.fetch = original; }
  });
});

test("the owner adds files: checked first, declared with their SHA-256, and listed with what happened", async () => {
  await withDOM(async ({ render, window }) => {
    const { Documents } = await loadComponent("src/components/book/documents.tsx");
    const original = globalThis.fetch, posts = [];
    let listed = [doc(1)];
    globalThis.fetch = async (url, init = {}) => {
      const u = new URL(String(url), "http://localhost");
      if (u.pathname === "/auth/session") return json({ mode: "local", user_id: "a".repeat(64), csrf: "token" });
      if (u.pathname === "/api/v1/documents" && init.method === "POST") {
        const bytes = new Uint8Array(init.body);
        posts.push({ book: u.searchParams.get("book_id"), headers: init.headers, size: bytes.length });
        const added = doc(9, { name: decodeURIComponent(init.headers["X-Vult-Document-Name"]), media_type: init.headers["Content-Type"], size: bytes.length, sha256: init.headers["X-Vult-Document-SHA256"] });
        listed = [added, ...listed];
        return json({ schema_version: 1, document: added, reused: false }, 201);
      }
      if (u.pathname === "/api/v1/documents") return json({ documents: listed, next_after: listed.length, has_more: false });
      return json({ error: "not found" }, 404);
    };
    try {
      await render(React.createElement(Documents, { book, onQueryChange() {} }));
      await settle();
      const root = document.getElementById("root");
      assert.match(root.textContent, /Drop files here/);
      const input = root.querySelector('input[type="file"]');
      const receipt = new window.File(["%PDF-1.7 synthetic"], "Lunch receipt.pdf", { type: "application/pdf" });
      // JSDOM's File has no arrayBuffer(); browsers do.
      Object.defineProperty(receipt, "arrayBuffer", { value: async () => new TextEncoder().encode("%PDF-1.7 synthetic").buffer });
      const photo = new window.File(["GIF89a"], "photo.gif", { type: "image/gif" });
      Object.defineProperty(input, "files", { value: [receipt, photo], configurable: true });
      await React.act(async () => input.dispatchEvent(new window.Event("change", { bubbles: true })));
      for (let i = 0; i < 40 && !root.querySelector('.document-upload-list li[data-state="added"]'); i++) await settle();
      assert.equal(posts.length, 1, "the GIF is refused before any request");
      const [post] = posts;
      assert.equal(post.book, book.id);
      assert.equal(post.headers["Content-Type"], "application/pdf");
      assert.equal(post.headers["X-Vult-Document-Name"], "Lunch%20receipt.pdf");
      assert.match(post.headers["X-Vult-Document-SHA256"], /^[a-f0-9]{64}$/);
      assert.ok(post.headers["Idempotency-Key"]);
      assert.equal(post.headers["X-CSRF-Token"] ?? Object.entries(post.headers).find(([k]) => /csrf/i.test(k))?.[1], "token");
      const log = root.querySelector(".document-upload-list").textContent;
      assert.match(log, /Lunch receipt\.pdf.*Added/);
      assert.match(log, /photo\.gif.*PDF, CSV, PNG, JPEG and WebP/);
      await React.act(() => new Promise((r) => setTimeout(r, 300)));
      assert.match(root.textContent, /Lunch receipt\.pdf/, "the list shows the new file");
    } finally { globalThis.fetch = original; }
  });
});

test("Sources uses the engine's matching count and cursor, then clears to the full list", async () => {
  await withDOM(async ({ render }) => {
    const { Documents } = await loadComponent("src/components/book/documents.tsx");
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url) => {
      const u = new URL(String(url), "http://localhost");
      assert.equal(u.pathname, "/api/v1/documents");
      const q = u.searchParams.get("q"), after = Number(u.searchParams.get("after") || 0);
      calls.push([q, after, u.searchParams.get("revision")]);
      const revision = "b".repeat(64);
      if (!q) return json({ documents: [doc(1)], next_after: 1, has_more: false });
      if (after) return json({ documents: [doc(105, { name: "audit-final.csv", created_at: "2026-10-05T10:00:00Z" })], next_after: 105, has_more: false, total: 51, revision });
      const documents = Array.from({ length: 50 }, (_, i) => doc(50 + i, { name: `audit-${i + 1}.pdf`, created_at: "2026-10-04T10:00:00Z" }));
      return json({ documents, next_after: 99, has_more: true, total: 51, revision });
    };
    try {
      const changes = [];
      const display = (query) => render(React.createElement(Documents, { book, query, onQueryChange: (q) => changes.push(q) }));
      // The results are a server-confirmed typo match, not a browser name filter.
      await display("auditt");
      await settle();
      const root = document.getElementById("root");
      assert.deepEqual(calls, [["auditt", 0, null]], "search does not sweep the list");
      assert.match(root.textContent, /audit-1\.pdf/);
      assert.ok(root.querySelector('[aria-label="51 sources"]'));
      await React.act(async () => root.querySelector('button[aria-label="Next page"]').click());
      await React.act(async () => root.querySelector('button[aria-label="Next page"]').click());
      await settle();
      assert.deepEqual(calls, [["auditt", 0, null], ["auditt", 99, "b".repeat(64)]]);
      assert.match(root.textContent, /audit-final\.csv/);
      assert.equal(root.querySelector('input[type="search"], [role="searchbox"]'), null);
      await React.act(async () => root.querySelector('button[aria-label="Remove search “auditt”"]').click());
      assert.deepEqual(changes, [""]);
      await display("");
      await settle();
      assert.deepEqual(calls.at(-1), [null, 0, null]);
      assert.match(root.textContent, /receipt-1\.pdf/);
    } finally { globalThis.fetch = original; }
  });
});

test("Sources restarts a stale cursor without retaining expired matches", async () => {
  await withDOM(async ({ render }) => {
    const { Documents } = await loadComponent("src/components/book/documents.tsx");
    const original = globalThis.fetch, cursors = [];
    globalThis.fetch = async (url) => {
      const q = new URL(String(url), "http://localhost").searchParams;
      assert.equal(q.get("q"), "audit");
      cursors.push(q.get("after"));
      if (q.has("after")) return json({ error: "changed" }, 409);
      if (cursors.length > 1) return json({
        documents: [doc(1, { name: "audit-current.pdf" })], total: 1, next_after: 1, has_more: false, revision: "b".repeat(64),
      });
      return json({
        documents: Array.from({ length: 50 }, (_, i) => doc(i + 2, { name: `audit-stale-${i}.pdf`, created_at: "2026-10-02T00:00:00Z" })),
        total: 51, next_after: 51, has_more: true, revision: "a".repeat(64),
      });
    };
    try {
      await render(React.createElement(Documents, { book, query: "audit", onQueryChange() {} }));
      const next = () => React.act(async () => document.querySelector('button[aria-label="Next page"]').click());
      await next();
      await next();
      await settle();
      assert.deepEqual(cursors, [null, "51", null]);
      assert.match(document.body.textContent, /audit-current\.pdf/);
      assert.doesNotMatch(document.body.textContent, /audit-stale/);
    } finally { globalThis.fetch = original; }
  });
});
