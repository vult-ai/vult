import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const { EntryDocuments } = await loadComponent("src/components/book/entry-document.tsx");
const id = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const bookId = id(1), entryId = id(2), doc = id(50);
const link = (n, action, revision) => ({ id: id(300 + n), link_id: id(400), revision, sequence: n, entry_id: entryId, document_id: doc,
  role: "supporting_invoice", action, actor_id: id(4), receipt_id: id(3), reason: "", recorded_at: "2025-03-03T10:00:00Z" });
const page = (evidence) => ({ version: 1, book_id: bookId, entry_id: entryId,
  documents: { sources: [], next_source_after: 0, source_has_more: false, evidence, next_evidence_after: evidence.at(-1)?.sequence || 0, evidence_has_more: false } });
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const settle = () => React.act(() => new Promise((done) => setTimeout(done, 0)));

test("a history toggle that fails keeps the rows labelled with their real mode, offers a retry, and then shows history", async () => {
  await withDOM(async ({ render }) => {
    const previous = globalThis.fetch;
    const waiting = [];
    globalThis.fetch = async (url) => {
      const path = String(url);
      if (path.includes(`/transactions/${entryId}/documents`)) return new Promise((resolve) => waiting.push({ path, resolve }));
      return json({}, 404); // Source names are optional here.
    };
    const answer = (body, status) => waiting.shift().resolve(json(body, status));
    const button = (name) => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === name);
    try {
      await render(React.createElement(EntryDocuments, { entryId, bookId, receipt: null }));
      await React.act(async () => answer(page([link(1, "attach", 1)])));
      await settle();
      assert.ok(button("Show history"), "current mode");

      await React.act(async () => button("Show history").click());
      await settle();
      assert.match(waiting[0].path, /history=true/);
      // While history is loading, the current rows are still labelled as current.
      assert.ok(button("Show history"));
      assert.equal(button("Hide history"), undefined);

      await React.act(async () => answer({ error: "unavailable" }, 503));
      await settle();
      const root = document.getElementById("root").textContent;
      assert.match(root, /History could not be loaded/);
      assert.ok(button("Show history"), "the rows still say they are current, not history");
      assert.ok(!/Unlinked/.test(root), "no history rows are claimed");
      const retry = [...document.querySelectorAll("button")].find((b) => /try again/i.test(b.textContent));
      assert.ok(retry, "a scoped retry");

      await React.act(async () => button("Show history").click());
      await settle();
      await React.act(async () => answer(page([link(1, "attach", 1), link(2, "detach", 2)])));
      await settle();
      assert.ok(button("Hide history"), "history is now shown, and labelled so");
      assert.match(document.getElementById("root").textContent, /Unlinked/);
      assert.doesNotMatch(document.getElementById("root").textContent, /could not be loaded/);
    } finally {
      globalThis.fetch = previous;
    }
  });
});
