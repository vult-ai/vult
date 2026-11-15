import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const wait = (ms = 30) => React.act(async () => new Promise((r) => setTimeout(r, ms)));
const open = { id: "11111111-1111-4111-8111-111111111111", name: "Eightball Inc.", currency: "USD", scale: 2, profile: "business" };
const put = { id: "22222222-2222-4222-8222-222222222222", name: "Old Shop", currency: "USD", scale: 2, profile: "business", archived: true };

test("Books lists archived Books last, marked, each with a way back; archiving asks the server and says when done", async (t) =>
  withDOM(async ({ render }) => {
    const sent = [];
    t.mock.method(globalThis, "fetch", async (url, init = {}) => {
      const path = new URL(String(url), "http://localhost").pathname;
      if (path === "/auth/session") return new Response(JSON.stringify({ mode: "local", csrf: "token" }), { status: 200 });
      if (path === "/api/v1/books/archive") { sent.push(JSON.parse(init.body)); return new Response(JSON.stringify({ book_id: put.id, archived: false }), { status: 200 }); }
      return new Response("{}", { status: 404 });
    });
    const { V0Books } = await loadComponent("src/components/v0/v0-books.tsx");
    let changed = 0;
    await render(React.createElement(V0Books, { books: [put, open], selected: open.id, onSelect: () => {}, onChanged: () => { changed++; } }));
    const rows = [...document.querySelectorAll(".v0-record-row")];
    assert.match(rows[0].textContent, /Eightball Inc\./, "open Books first");
    assert.match(rows[1].textContent, /Old Shop.*Archived/);
    const unarchive = [...rows[1].querySelectorAll("button")].find((b) => b.textContent.trim() === "Unarchive");
    assert.ok(unarchive, "an archived Book has Unarchive");
    assert.ok([...rows[0].querySelectorAll("button")].some((b) => b.textContent.trim() === "Archive"));
    await React.act(async () => unarchive.click());
    await wait();
    assert.deepEqual(sent, [{ book_id: put.id, archived: false }]);
    assert.equal(changed, 1);
  }));

test("the Book menu leaves archived Books out, unless it's the one open", async () =>
  withDOM(async ({ render }) => {
    const { BookSwitcher } = await loadComponent("src/components/book/book-switcher.tsx");
    await render(React.createElement(BookSwitcher, { books: [open, put], selected: open.id, onSelect: () => {} }));
    await React.act(async () => document.querySelector(".book-switcher button").click());
    await wait();
    const names = [...document.querySelectorAll('[role="menuitemradio"], [role="menuitem"], [role="option"]')].map((n) => n.textContent);
    assert.ok(names.some((n) => n.includes("Eightball Inc.")), names.join(" | "));
    assert.ok(!names.some((n) => n.includes("Old Shop")), names.join(" | "));
  }));
