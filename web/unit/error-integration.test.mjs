import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const currencies = [
  { code: "USD", name: "US Dollar", scale: 2 },
  { code: "EUR", name: "Euro", scale: 2 },
  { code: "PLN", name: "Polish Zloty", scale: 2 },
  { code: "JPY", name: "Yen", scale: 0 },
  { code: "KWD", name: "Kuwaiti Dinar", scale: 3 },
];
const book = (id) => ({
  id,
  profile: "personal",
  name: "Synthetic Personal",
  founder: "Example",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_timezone: "UTC",
});
const account = (code, currency = "USD", extra = {}) => ({
  code,
  name: `${currency} ${code}`,
  currency,
  scale: currencies.find((item) => item.code === currency).scale,
  native_minor: "0",
  carrying_minor: "0",
  ...extra,
});
const state = (book) => ({
  book,
  currencies,
  cash_accounts: [account("1000")],
  accounts: [
    { code: "1000", name: "Bank", kind: "asset" },
    { code: "2000", name: "Card payable", kind: "liability" },
    { code: "3100", name: "Opening equity", kind: "equity" },
    { code: "4000", name: "Income", kind: "revenue" },
    { code: "5000", name: "Groceries", kind: "expense" },
  ],
  personal: {
    as_of: "2026-02-06",
    currency: "USD",
    scale: 2,
    assets_minor: "136000",
    liabilities_minor: "15000",
    tracked_net_worth_minor: "121000",
    basis: "Recorded historical carrying amounts; no market prices.",
    scope: "Only accounts recorded in this Book; not complete personal wealth.",
    included_account_codes: ["1000", "2000"],
    card_accounts: [
      account("2000", "USD", {
        native_minor: "15000",
        carrying_minor: "15000",
        issuer_owes: false,
      }),
    ],
    indicative_liquid_estimate: null,
  },
  summary: {
    cash_minor: "136000",
    reimbursements_minor: "0",
    loans_minor: "0",
    owed_minor: "0",
    income_minor: "50000",
    expenses_minor: "9000",
  },
  payables: [],
  transactions: [],
  fx_gain_minor: "0",
  fx_loss_minor: "0",
});
async function fill(window, selector, value) {
  await React.act(async () => {
    const node = document.querySelector(selector);
    assert.ok(node, selector);
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(node, value);
    node.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}
async function choose(window, selector, value) {
  await React.act(async () => {
    const node = document.querySelector(selector);
    assert.ok(node, selector);
    node.value = value;
    node.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
}
const waitPreview = () =>
  React.act(async () => new Promise((resolve) => setTimeout(resolve, 340)));
const submit = (window) =>
  React.act(async () =>
    document
      .querySelector("form")
      .dispatchEvent(
        new window.Event("submit", { bubbles: true, cancelable: true }),
      ),
  );
const response = (data) => ({ ok: true, json: async () => data });
async function attachSource() {
  const button = name => [...document.querySelectorAll("button")].find(e => e.textContent.trim() === name);
  await React.act(async () => button("Attach from Sources").click());
  await React.act(async () => { await new Promise(resolve => setTimeout(resolve, 15)); });
  await React.act(async () => document.querySelector(".source-attachment-option input").click());
  await React.act(async () => button("Attach 1 file").click());
}

test("legacy POST uses normalized outcomes and keeps the original multipart financial request", async () => {
  await withDOM(async ({ window }) => {
    const { post, isConfirmedRejection } = await loadComponent("src/lib/api.ts");
    const original = globalThis.fetch, sent = [];
    const body = { book_id: "synthetic", date: "2026-10-12", amount_minor: "1200", source_ids: ["source-a"], idempotency_key: "frozen-key" };
    const receipt = new window.File(["synthetic bytes"], "receipt.pdf", { type: "application/pdf" });
    try {
      for (const status of [408, 409, 429, 500, 503, 200]) {
        globalThis.fetch = async (url, init) => {
          if (url.endsWith("/session")) return response({ csrf: "synthetic-csrf" });
          sent.push({ request: init.body.get("request"), file: init.body.get("file"), csrf: init.headers["X-CSRF-Token"] });
          return new Response(status === 200 ? "truncated JSON" : JSON.stringify({ error: "SQLSTATE private endpoint" }), { status });
        };
        await assert.rejects(post("transactions", body, receipt), error => {
          assert.equal(error.failure.outcome, "unknown");
          assert.equal(isConfirmedRejection(error), false);
          assert.doesNotMatch(error.message, /SQLSTATE|private|JSON|endpoint/);
          return true;
        });
      }
      assert.equal(sent.length, 6, "one POST per explicit attempt, no automatic retry");
      for (const item of sent) {
        assert.equal(item.request, JSON.stringify(body));
        assert.equal(item.file.name, receipt.name); assert.equal(item.file.size, receipt.size);
        assert.equal(item.csrf, "synthetic-csrf");
      }
    } finally { globalThis.fetch = original; }
  });
});
