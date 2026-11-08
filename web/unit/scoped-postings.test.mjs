import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, React, withDOM } from "./helpers/dom.mjs";

const book = { id: "book", currency: "USD", scale: 2 };
const entry = (id, amount = "123") => ({
  id,
  lines: [
    {
      id: `${id}-debit`,
      account_code: "1000",
      side: "debit",
      amount_minor: amount,
    },
    {
      id: `${id}-credit`,
      account_code: "4000",
      side: "credit",
      amount_minor: amount,
    },
  ],
});
const reply = (ids, current = book) => ({
  version: 1,
  book_id: current.id,
  currency: current.currency,
  scale: current.scale,
  content_sha256: "a".repeat(64),
  entries: ids.map((id) => entry(id)),
});
const response = (data) => ({
  ok: true,
  headers: new Headers({ "content-type": "application/json" }),
  json: async () => data,
});

test("scoped posting parser preserves exact complete journal facts and rejects false scope or precision", async () => {
  const { parseScopedPostings, parseTransactionJournal } = await loadComponent(
    "src/lib/transaction-journal.ts",
  );
  const data = reply(["a"]);
  data.entries[0].lines[0].amount_minor = "9007199254740993";
  data.entries[0].lines[1].amount_minor = "9007199254740993";
  const csv =
    "book_id,currency,transaction_id,line_id,account_code,side,amount_minor\r\nbook,USD,a,a-debit,1000,debit,9007199254740993\r\nbook,USD,a,a-credit,4000,credit,9007199254740993\r\n";
  assert.deepEqual(
    parseScopedPostings(data, book, ["a"]),
    parseTransactionJournal(csv, book),
  );
  const bad = [
    { ...data, book_id: "other" },
    { ...data, currency: "EUR" },
    { ...data, scale: 3 },
    { ...data, version: 2 },
    { ...data, entries: [] },
    { ...data, entries: [entry("extra")] },
    { ...data, entries: [entry("a"), entry("a")] },
    { ...data, content_sha256: "" },
  ];
  for (const v of bad) assert.throws(() => parseScopedPostings(v, book, ["a"]));
  for (const mutation of [
    (e) => {
      e.lines[1].amount_minor = "124";
    },
    (e) => {
      e.lines[1].id = e.lines[0].id;
    },
    (e) => {
      e.lines[0].amount_minor = 123;
    },
    (e) => {
      e.lines[0].amount_minor = "9223372036854775808";
    },
    (e) => {
      e.lines[0].side = "other";
    },
  ]) {
    const v = reply(["a"]);
    mutation(v.entries[0]);
    assert.throws(() => parseScopedPostings(v, book, ["a"]));
  }
});

test("Activity requests only missing visible IDs and retains cached pages without full exports", async () => {
  await withDOM(async ({ render, window }) => {
    const { useTransactionJournal } = await loadComponent(
      "src/lib/use-transaction-journal.ts",
    );
    const old = globalThis.fetch,
      calls = [];
    let read;
    globalThis.fetch = async (url) => {
      const u = new URL(url, window.location.href);
      calls.push(u);
      assert.equal(u.pathname, "/api/v1/transaction-postings");
      return response(
        reply(u.searchParams.get("ids").split(","), {
          ...book,
          id: u.searchParams.get("book_id"),
        }),
      );
    };
    function View({
      ids = ["a"],
      current = book,
      revision = 0,
      active = true,
    }) {
      read = useTransactionJournal(current, revision, active, ids);
      return React.createElement("div", null, read.journal?.size || "pending");
    }
    try {
      await render(React.createElement(View));
      await React.act(async () => {});
      assert.equal(calls.length, 1);
      assert.ok(read.journal.has("a"));
      await render(React.createElement(View, { ids: ["a", "b"] }));
      await React.act(async () => {});
      assert.equal(calls.at(-1).searchParams.get("ids"), "b");
      assert.ok(read.journal.has("a") && read.journal.has("b"));
      await render(React.createElement(View));
      await render(React.createElement(View, { active: false }));
      await render(React.createElement(View));
      assert.equal(calls.length, 2);
      await render(React.createElement(View, { revision: 1 }));
      await React.act(async () => {});
      assert.equal(calls.length, 3);
      await render(
        React.createElement(View, {
          current: { ...book, id: "other" },
          revision: 1,
        }),
      );
      await React.act(async () => {});
      assert.equal(calls.at(-1).searchParams.get("book_id"), "other");
    } finally {
      globalThis.fetch = old;
    }
  });
});
