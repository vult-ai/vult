import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Synthetic", address: { book_id: "11111111-1111-4111-8111-111111111111", code: "K7FM", slug: "synthetic", revision: 0, aliases: ["k7fm", "synthetic"] } };
const entry = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", reference: "7M4X9Q2Z" };
const next = { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", reference: "N8K2WP3R" };
const range = { from: "2025-10-01", to: "2026-09-30" };
const dates = "from=2025-10-01&to=2026-09-30";

test("a bare Accounts link uses the same period before and after Book routing", () => withDOM(async ({ window }) => {
  const m = await loadComponent("src/lib/dashboard-location.ts");
  const read = () => m.readDashboardLocation("2020-01-01", "2026-11-07", book);
  window.history.replaceState(null, "", "/accounts");
  const before = read();
  window.history.replaceState(null, "", "/synthetic/accounts");
  assert.equal(before.period, "last-30-days");
  assert.deepEqual(read(), before);
}));

test("Accounts removes inactive statement parameters without changing the confirmed interval, filters or history", () => withDOM(async ({ window }) => {
  const m = await loadComponent("src/lib/dashboard-location.ts");
  window.history.replaceState({ retained: "inspector" }, "", `/synthetic/accounts?as_of=2026-09-30&${dates}&report=balance_sheet&compare=explicit&compare_to=2025-09-30&columns=months&group=asset`);
  const before = m.readDashboardLocation("2020-01-01", "2026-10-29", book);
  assert.deepEqual(before.range, range);
  m.writeDashboardLocation(book, before, "replace");
  assert.equal(window.location.search, `?group=asset&${dates}`);
  assert.deepEqual(m.readDashboardLocation("2020-01-01", "2026-10-29", book), before);
  assert.deepEqual(window.history.state, { retained: "inspector" });
}));

test("copied Accounts links identify the confirmed activity interval while Statements retains its position context", () => withDOM(async () => {
  const m = await loadComponent("src/lib/dashboard-location.ts");
  const account = new URL(m.currentViewURL(book, range, `http://localhost/synthetic/accounts?as_of=2026-09-30&${dates}&entry=${entry.reference}`));
  assert.equal(account.searchParams.has("as_of"), false);
  assert.equal(account.searchParams.get("entry"), entry.reference);
  assert.equal(account.searchParams.get("from"), range.from);
  assert.equal(account.searchParams.get("to"), range.to);
  const statement = new URL(m.currentViewURL(book, range, "http://localhost/synthetic/statements?as_of=2026-09-30&report=balance_sheet"));
  assert.equal(statement.searchParams.get("as_of"), "2026-09-30");
}));

test("verified Accounts entry links drop the duplicate Book UUID and close without losing dates or mounted content", () => withDOM(async ({ window, render }) => {
  const { useResourcePreview } = await loadComponent("src/lib/use-resource-preview.ts");
  const datesModel = await loadComponent("src/lib/dashboard-location.ts");
  const saved = globalThis.fetch, reads = [];
  globalThis.fetch = async url => { reads.push(String(url)); throw new Error("A loaded verified reference needs no lookup"); };
  let value;
  function Harness() { value = useResourcePreview(book, "entry", false, [entry, next]); return React.createElement("textarea", { defaultValue: "Retained draft" }); }
  try {
    window.history.replaceState({ retained: "history" }, "", `/synthetic/accounts?as_of=2026-09-30&${dates}&entry=${entry.reference}&entryBook=${book.id}`);
    await render(React.createElement(Harness));
    const selection = datesModel.readDashboardLocation("2020-01-01", "2026-10-29", book);
    await React.act(async () => datesModel.writeDashboardLocation(book, selection, "replace"));
    assert.equal(value.detail, entry.id);
    assert.equal(window.location.pathname, "/synthetic/accounts");
    assert.equal(new URLSearchParams(window.location.search).has("entryBook"), false);
    assert.equal(new URLSearchParams(window.location.search).has("as_of"), false);
    const draft = document.querySelector("textarea"); draft.value = "Unsaved review";
    await React.act(async () => value.show(next.id));
    assert.equal(new URLSearchParams(window.location.search).get("entry"), next.reference);
    assert.deepEqual(window.history.state, { retained: "history" });
    await React.act(async () => value.show("", "replace"));
    assert.equal(window.location.search, `?${dates}`);
    assert.equal(document.querySelector("textarea"), draft);
    assert.equal(draft.value, "Unsaved review");
    assert.equal(reads.length, 0);
  } finally { globalThis.fetch = saved; }
}));

test("legacy unscoped contextual entries keep explicit Book scope; scoped Statements keep their report date", () => withDOM(async ({ window, render }) => {
  const { useResourcePreview } = await loadComponent("src/lib/use-resource-preview.ts");
  let value;
  function Harness() { value = useResourcePreview(book, "entry", false, [entry]); return null; }
  window.history.replaceState(null, "", `/accounts?${dates}&entry=${entry.reference}&entryBook=${book.id}`);
  await render(React.createElement(Harness, { key: "legacy" }));
  assert.equal(new URLSearchParams(window.location.search).get("entryBook"), book.id);
  window.history.replaceState(null, "", `/synthetic/statements?as_of=2026-09-30&entry=${entry.reference}&entryBook=${book.id}`);
  await render(React.createElement(Harness, { key: "scoped" }));
  assert.equal(value.detail, entry.id);
  assert.equal(new URLSearchParams(window.location.search).get("as_of"), "2026-09-30");
  assert.equal(new URLSearchParams(window.location.search).has("entryBook"), false);
}));

test("foreign Book entry selectors stay rejected rather than being cleaned into a different financial scope", () => withDOM(async ({ window, render }) => {
  const { useResourcePreview } = await loadComponent("src/lib/use-resource-preview.ts");
  let value;
  function Harness() { value = useResourcePreview(book, "entry", false, [entry]); return null; }
  window.history.replaceState(null, "", `/synthetic/accounts?${dates}&entry=${entry.reference}&entryBook=other-book`);
  await render(React.createElement(Harness));
  assert.equal(value.detail, "");
  assert.equal(new URLSearchParams(window.location.search).get("entryBook"), "other-book");
}));

test("Accounts contextual entry history preserves pending drafts and restores the same interval", () => withDOM(async ({ window, render }) => {
  const { useResourcePreview } = await loadComponent("src/lib/use-resource-preview.ts");
  let value, pending = false;
  function Harness() { value = useResourcePreview(book, "entry", pending, [entry, next]); return React.createElement("textarea", { defaultValue: "Draft" }); }
  window.history.replaceState({ retained: "account" }, "", `/synthetic/accounts?${dates}`);
  await render(React.createElement(Harness));
  const draft = document.querySelector("textarea"); draft.value = "Keep this correction";
  await React.act(async () => value.show(entry.id));
  const selected = window.location.href;
  pending = true; await render(React.createElement(Harness));
  await React.act(async () => { window.history.replaceState({}, "", `/synthetic/accounts?${dates}`); window.dispatchEvent(new window.PopStateEvent("popstate")); });
  assert.equal(window.location.href, selected);
  assert.equal(value.detail, entry.id);
  pending = false; await render(React.createElement(Harness));
  await React.act(async () => { window.history.replaceState({ retained: "account" }, "", `/synthetic/accounts?${dates}`); window.dispatchEvent(new window.PopStateEvent("popstate")); });
  assert.equal(value.detail, "");
  assert.equal(document.querySelector("textarea"), draft);
  assert.equal(draft.value, "Keep this correction");
  assert.equal(new URLSearchParams(window.location.search).has("entryBook"), false);
}));
