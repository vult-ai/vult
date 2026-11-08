import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const c = await loadComponent("unit/helpers/open-contracts.tsx");
const id = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const book = { id: id(1), currency: "USD", scale: 2, name: "Northwind Studio", founder: "", start_date: "" };
const cash = id(10), fees = id(11);
const entry = {
  id: id(2), sequence: 7, receipt_id: id(3), actor_id: id(4),
  economic_date: "2025-03-01", accounting_date: "2025-03-03", description: "Supplier invoice",
  recorded_at: "2025-03-03T10:00:00Z",
  lines: [
    { id: id(20), account_id: fees, side: "debit", amount_minor: "10850",
      native: { currency: "EUR", scale: 2, amount_minor: "10000" },
      valuation: { basis: "quoted_rate", policy: "quoted-rate-half-even-v1",
        input: { currency: "EUR", scale: 2, amount_minor: "10000" },
        rate: { base: "EUR", quote: "USD", rate: "1.085", requested_date: "2025-03-01", observation_date: "2025-02-28", provenance: "reference" } },
      contexts: [{ role: "invoice_total", amount: { currency: "EUR", scale: 2, amount_minor: "10000" } }] },
    { id: id(21), account_id: cash, side: "credit", amount_minor: "10850" },
  ],
};
const read = { version: 1, book_id: book.id, currency: "USD", scale: 2, entry };

test("the complete entry is accepted whole and refused when anything is missing or unbalanced", () => {
  assert.equal(c.isJournalRead(read), true);
  assert.deepEqual(c.parseJournalRead(read, book, entry.id), read);
  const broken = {
    "other Book": { ...read, book_id: id(9) },
    "other entry": { ...read, entry: { ...entry, id: id(9) } },
    "unbalanced": { ...read, entry: { ...entry, lines: [entry.lines[0], { ...entry.lines[1], amount_minor: "10849" }] } },
    "one line": { ...read, entry: { ...entry, lines: [entry.lines[0]] } },
    "missing accounting date": { ...read, entry: { ...entry, accounting_date: undefined } },
    "decimal amount": { ...read, entry: { ...entry, lines: [{ ...entry.lines[0], amount_minor: "108.50" }, entry.lines[1]] } },
    "unknown field": { ...read, entry: { ...entry, fx: {} } },
    "bad native": { ...read, entry: { ...entry, lines: [{ ...entry.lines[0], native: { currency: "EUR", amount_minor: "1" } }, entry.lines[1]] } },
  };
  for (const [name, value] of Object.entries(broken)) assert.throws(() => c.parseJournalRead(value, book, entry.id), undefined, name);
});

test("the journal accepts the engine's recorded state and strictly scoped recorder metadata", () => {
  const engine = {
    ...read, schema_version: 1, state_revision: "a".repeat(64),
    state_captured_at: "2025-03-03T10:00:01.123456Z",
  };
  const owner = { kind: "owner", name: "Example owner" };
  const agent = { kind: "agent", name: "Example agent", connection_id: id(7) };
  for (const value of [engine, { ...engine, recorded_by: owner }, { ...engine, recorded_by: agent }])
    assert.deepEqual(c.parseJournalRead(value, book, entry.id), value);
  for (const value of [
    { ...engine, schema_version: 2 },
    { ...engine, state_revision: null },
    { ...engine, state_captured_at: "2025-03-03" },
    { ...engine, recorded_by: null },
    { ...engine, recorded_by: { ...owner, name: "" } },
    { ...engine, recorded_by: { ...owner, connection_id: agent.connection_id } },
    { ...engine, recorded_by: { ...agent, connection_id: "other" } },
    { ...engine, recorded_by: { ...agent, photo: "unexpected" } },
    { ...engine, extra: true },
  ]) assert.throws(() => c.parseJournalRead(value, book, entry.id));
});

test("the entry shows every posting with its native amount, valuation, context and three dates", async () => {
  await withDOM(async ({ render }) => {
    const previous = globalThis.fetch;
    globalThis.fetch = async () => new Response("[]", { headers: { "Content-Type": "application/json" } });
    try {
      const state = { accounts: [{ code: fees, name: "Expenses:Professional fees", kind: "expense", gl_code: "6100" }, { code: cash, name: "Operating", kind: "asset" }] };
      const panel = (recorded_by) => render(React.createElement(c.LedgerEntry, {
        read: { ...read, recorded_by }, state, book, onNavigate() {},
      }));
      await panel();
      const root = document.getElementById("root");
      const text = root.textContent;
      for (const expected of ["Professional fees", "6100", "Operating", "€100.00", "native", "1.085", "Invoice total", "Accounting date", "Economic date", "Recorded at"])
        assert.ok(text.includes(expected), expected);
      assert.ok(!text.includes(fees), "an account ID is never shown as its code");
      assert.equal(root.querySelector('[aria-label="Recorder"]'), null);
      await panel({ kind: "owner", name: "Example owner" });
      assert.match(root.querySelector('[aria-label="Recorder"]').textContent, /Recorded by Example owner/);
      await panel({ kind: "agent", name: "Example agent", connection_id: id(7) });
      const attribution = root.querySelector('[aria-label="Recorder"]');
      assert.match(attribution.textContent, /Recorded by Example agent/);
      assert.ok(attribution.querySelector("[data-counterparty-avatar]"));
      assert.equal(attribution.querySelector("img"), null);
    } finally {
      globalThis.fetch = previous;
    }
  });
});

const citation = (n, doc) => ({ id: id(100 + n), source_id: id(200 + n), revision: 1, sequence: n, entry_id: entry.id, position: 0, document_id: doc, location: { page: 2, row: n }, actor_id: id(4), receipt_id: id(3), reason: "Invoice line", recorded_at: "2025-03-03T10:00:00Z" });
const link = (n, doc, action = "attach") => ({ id: id(300 + n), link_id: id(400), revision: action === "attach" ? 1 : 2, sequence: n, entry_id: entry.id, document_id: doc, role: "supporting_invoice", action, actor_id: id(4), receipt_id: id(3), reason: "", recorded_at: "2025-03-03T10:00:00Z" });
const docs = (sources, evidence, more = {}) => ({ version: 1, book_id: book.id, entry_id: entry.id, documents: { sources, next_source_after: sources.at(-1)?.sequence || 0, source_has_more: false, evidence, next_evidence_after: evidence.at(-1)?.sequence || 0, evidence_has_more: false, ...more } });

test("document relationships keep both cursors, their roles and their places", () => {
  const doc = id(500);
  const first = c.parseEntryDocuments(docs([citation(1, doc)], [link(1, doc)], { source_has_more: true }), book.id, entry.id);
  const second = c.parseEntryDocuments(docs([citation(1, doc), citation(2, doc)], []), book.id, entry.id);
  const merged = c.mergeEntryDocuments(first, second);
  assert.equal(merged.sources.length, 2, "a repeated citation is kept once");
  assert.equal(merged.evidence.length, 1, "the evidence list is untouched by a source page");
  assert.equal(merged.source_has_more, false);
  const [row] = c.relatedDocuments(merged);
  assert.equal(row.citations.length, 2);
  assert.equal(c.locationLabel(row.citations[0].location), "Page 2, row 1");
  const query = c.entryDocumentsQuery({ source_after: 5, evidence_after: 9, history: true });
  assert.equal(query.get("source_after"), "5");
  assert.equal(query.get("evidence_after"), "9");
  assert.equal(query.get("history"), "true");
  for (const bad of [
    docs([{ ...citation(1, doc), entry_id: id(9) }], []),
    docs([], [{ ...link(1, doc), action: "remove" }]),
    { ...docs([], []), book_id: id(9) },
    [{ id: doc, filename: "invoice.pdf" }],
  ]) assert.throws(() => c.parseEntryDocuments(bad, book.id, entry.id));
});

test("special accounts come from recorded roles; legacy codes count only in a chart without roles", () => {
  const legacy = [{ code: "4100" }, { code: "5100" }, { code: "1000" }];
  assert.equal(c.systemAccount(legacy, "fx_gain"), "4100");
  const open = [{ code: id(1), role: "fx_gain" }, { code: "4100" }];
  assert.equal(c.systemAccount(open, "fx_gain"), id(1));
  assert.equal(c.systemAccount(open, "fx_loss"), undefined, "a roled chart never falls back to a code");
  assert.deepEqual([...c.systemAccounts(legacy, ["fx_gain", "fx_loss"])].sort(), ["4100", "5100"]);
});

test("missing native history is unavailable, never zero", () => {
  const account = { currency: "EUR", scale: 2 };
  assert.equal(c.nativeMinor({ ...account, native_minor: "1200", native_status: "complete" }), "1200");
  assert.equal(c.nativeMinor({ ...account, native_minor: null, native_status: "incomplete" }), null);
  assert.equal(c.nativeMinor({ ...account, native_minor: "0", native_status: "not_recorded" }), null);
  assert.equal(c.nativeMinor({ ...account, native_minor: "" }), null);
  assert.equal(c.nativeMoney({ ...account, native_minor: null, native_status: "incomplete" }), "Not available");
  assert.match(c.nativeMoney({ ...account, native_minor: "1200", native_status: "complete" }), /12\.00/);
});
