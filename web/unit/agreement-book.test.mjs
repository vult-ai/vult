import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import { book as fixture, performance, position } from "./helpers/agreement-oracle.mjs";

/* The interface half of the agreement proof. The route responses below carry the
   oracle's answers in the engine's published shapes; the engine half checks that the
   real routes return those same answers for the same Book. Together they cover engine,
   adapters, cards, chart and table. Expected strings are written out by hand. */
const { V0Overview, readBookState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
const book = { id: "22222222-2222-4222-8222-222222222222", name: fixture.book.name, start_date: "2026-01-01", currency: "USD", scale: 2, accounting_timezone: fixture.book.accounting_timezone };
const S = (v) => String(v);
const STATE = "state-1";
const accountsByKey = new Map(fixture.accounts.map((a) => [a.key, a]));

const sideDTO = (s) => ({
  total_minor: S(s.total), positive_minor: S(s.positive), negative_minor: S(s.negative),
  accounts: s.accounts.map((a) => ({ account_id: a.key, name: a.name, amount_minor: S(a.amount), positive_minor: S(a.positive), negative_minor: S(a.negative), posting_count: a.postings })),
});
function snapshot(from, to, unit) {
  const p = performance(from, to, unit), pos = position(to);
  const rows = fixture.accounts.filter((a) => ["asset", "liability"].includes(a.class))
    .map((a) => ({ code: a.key, name: a.name, kind: a.class, amount: S(pos.balances[a.key]) }));
  const previousYear = (d) => `${Number(d.slice(0, 4)) - 1}${d.slice(4)}`;
  return {
    schema_version: 1, book_id: book.id, period: { from, to }, currency: book.currency, scale: book.scale,
    cash_minor: S(pos.cash), cash_status: "partial",
    cash_accounts: fixture.accounts.filter((a) => a.role === "cash").map((a) => ({ code: a.key, name: a.name, carrying_minor: S(pos.balances[a.key]) })),
    prior_year: { income: "0", expenses: "0", result: "0", period: { from: previousYear(from), to: previousYear(to) } },
    trends: { current: [], previous: [] },
    financial: {
      assets: S(pos.assets), liabilities: S(pos.liabilities), equity: S(pos.equity), equityAccounts: S(pos.balances.capital), accumulatedResult: S(pos.result),
      income: S(p.income.total), expenses: S(p.expenses.total), result: S(p.income.total - p.expenses.total), through: to, rows, incomeRows: [], expenseRows: [],
    },
    overviewComparison: { current: [], previous: [], range: { from, to }, previousRange: { from, to } },
    performance: {
      status: "recorded", period: { from, to }, unit, timezone: book.accounting_timezone,
      income_minor: S(p.income.total), expenses_minor: S(p.expenses.total),
      buckets: p.buckets.map((b) => ({ from: b.from, to: b.to, covered_from: b.covered_from, covered_to: b.covered_to, income: sideDTO(b.income), expenses: sideDTO(b.expenses) })),
    },
  };
}
const state = {
  book, accounts: [], cash_accounts: [], transactions: [], currencies: [{ code: "USD", scale: 2, name: "US Dollar" }],
  state_revision: STATE,
};
const json = (body) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
const requests = [];
/* The engine's own answers for this Book, captured from the real routes (performance
   only; card figures come from the oracle). Contributions are signed there:
   amount_minor = positive_minor + negative_minor, negative_minor <= 0. */
const captured = (name) => JSON.parse(readFileSync(new URL(`./fixtures/agreement-real/${name}.json`, import.meta.url), "utf8")).performance;
const realPerformance = {
  "2026-01-01/2026-03-31/week": captured("q1_weeks"),
  "2026-01-01/2026-03-31/month": captured("q1_months"),
  "2026-01-01/2026-03-31/quarter": captured("q1_quarter"),
  "2026-01-01/2026-04-30/month": captured("jan_apr_months"),
};
function server(window, real = false) {
  return async (url) => {
    const u = new URL(String(url), window.location.href), q = u.searchParams, path = u.pathname.replace("/api/v1/", "");
    requests.push(`${path}?${q}`);
    if (path === "state") {
      const from = q.get("dashboard_from"), to = q.get("dashboard_to"), unit = q.get("dashboard_unit");
      const body = snapshot(from, to, unit);
      const engine = realPerformance[`${from}/${to}/${unit}`];
      if (real && engine) body.performance = engine;
      return json({ ...state, dashboard_snapshot: body });
    }
    return new Response("{}", { status: 404 });
  };
}
const panel = (name) => [...document.querySelectorAll(".dash-panel")].find((p) => p.getAttribute("aria-label") === name);
const headline = (name) => panel(name)?.querySelector(".vult-headline .rolling-number-text")?.textContent;
const stat = (name, label) => [...panel(name).querySelectorAll(".dash-stat")]
  .find((s) => s.querySelector(".dash-stat-label").textContent.trim() === label)
  ?.querySelector(".dash-figure .rolling-number-text")?.textContent;
const settle = () => React.act(() => new Promise((done) => setTimeout(done, 30)));
async function open(from, to, window, render) {
  window.history.replaceState(null, "", `/dashboard?from=${from}&to=${to}`);
  const opened = await readBookState(book.id, {}, book.accounting_timezone, { view: "dashboard", range: { from, to } });
  await render(React.createElement(V0Overview, { page: "dashboard", state: opened, book, onNavigate() {}, onShowTransactions() {}, onInspect() {} }));
  for (let i = 0; i < 10 && !headline("Income and expenses")?.match(/\d/); i++) await settle();
}
async function table() {
  const toggle = [...panel("Income and expenses").querySelectorAll("button")].find((b) => b.textContent === "Show table");
  await React.act(async () => toggle.click());
  const cells = (sel) => [...panel("Income and expenses").querySelectorAll(sel)].map((r) => [...r.children].map((c) => c.textContent));
  return { rows: cells(".dash-table tbody tr"), total: cells(".dash-table tfoot tr")[0] };
}

test("first quarter in weeks: every card, the chart and its table agree with the hand-worked answers", async () => {
  await withDOM(async ({ render, window }) => {
    const original = globalThis.fetch;
    globalThis.fetch = server(window);
    try {
      await open("2026-01-01", "2026-03-31", window, render);
      assert.equal(requests.length, 1);
      assert.match(requests[0], /^state\?.*dashboard_unit=week/, "asks for Monday weeks");
      assert.equal(stat("Summary", "Income"), "$7,000.00");
      assert.equal(stat("Summary", "Expenses"), "$5,020.00");
      assert.equal(stat("Summary", "Net result"), "$1,980.00");
      assert.equal(headline("Income and expenses"), "$1,980.00");
      assert.equal(headline("Where the money went"), "$5,020.00");
      assert.equal(stat("Summary", "Cash"), "$11,780.00", "cash on March 31, not today's $12,680.00");
      assert.equal(headline("Cash by account"), "$11,780.00");
      assert.equal(headline("What you owe"), "$2,800.00", "card $300.00 and loan $2,500.00 on March 31");
      assert.equal(stat("Balance sheet", "Liabilities"), "$2,800.00");
      assert.equal(stat("Balance sheet", "Assets"), "$14,780.00");
      // Refunds drawn below zero: Software in the weeks of Jan 19 and Feb 16, Travel in the week of Mar 30.
      assert.equal(panel("Income and expenses").querySelectorAll(".dash-segment-negative").length, 3);
      // Software is negative for the quarter (100.00 - 30.00 - 250.00 + 50.00) and is listed, not dropped.
      const software = [...panel("Where the money went").querySelectorAll(".dash-row")].find((r) => r.textContent.includes("Software"));
      assert.match(software?.textContent ?? "", /Refunds exceeded spending.*−\$130\.00/);
      const { rows, total } = await table();
      assert.equal(rows.length, 14, "fourteen Monday weeks, partial at both ends");
      assert.deepEqual(rows[2].slice(1), ["$0.00", "$100.00", "−$100.00"], "week of Jan 12: the 100.00 expense");
      assert.deepEqual(rows[3].slice(1), ["$0.00", "−$30.00", "$30.00"], "week of Jan 19: the 30.00 refund");
      assert.deepEqual(rows[13].slice(1), ["$0.00", "$0.00", "$0.00"], "week of Mar 30: a reclassification, zero net with activity");
      assert.deepEqual(total.slice(1), ["$7,000.00", "$5,020.00", "$1,980.00"]);
    } finally {
      globalThis.fetch = original;
      requests.length = 0;
    }
  });
});

test("January to April in months: March nets to zero and April changes only the later balances", async () => {
  await withDOM(async ({ render, window }) => {
    const original = globalThis.fetch;
    globalThis.fetch = server(window);
    try {
      await open("2026-01-01", "2026-04-30", window, render);
      assert.equal(requests.length, 1);
      assert.match(requests[0], /^state\?.*dashboard_unit=month/);
      const { rows, total } = await table();
      assert.deepEqual(rows.map((r) => r.slice(1)), [
        ["$4,000.00", "$1,570.00", "$2,430.00"],
        ["$1,200.00", "$1,650.00", "−$450.00"],
        ["$1,800.00", "$1,800.00", "$0.00"],
        ["$900.00", "$200.00", "$700.00"],
      ]);
      assert.deepEqual(total.slice(1), ["$7,900.00", "$5,220.00", "$2,680.00"]);
      assert.equal(stat("Summary", "Cash"), "$12,680.00");
      assert.equal(headline("What you owe"), "$3,000.00");
    } finally {
      globalThis.fetch = original;
      requests.length = 0;
    }
  });
});

test("the engine's real Q1 weeks: signed refunds read exactly in the chart and its table", async () => {
  await withDOM(async ({ render, window }) => {
    const original = globalThis.fetch;
    globalThis.fetch = server(window, true);
    try {
      await open("2026-01-01", "2026-03-31", window, render);
      assert.equal(headline("Income and expenses"), "$1,980.00");
      assert.equal(headline("Where the money went"), "$5,020.00");
      assert.equal(panel("Income and expenses").querySelectorAll(".dash-segment-negative").length, 3);
      const { rows, total } = await table();
      assert.equal(rows.length, 14);
      assert.deepEqual(rows[3].slice(1), ["$0.00", "−$30.00", "$30.00"], "week of Jan 19: the 30.00 refund");
      assert.deepEqual(total.slice(1), ["$7,000.00", "$5,020.00", "$1,980.00"]);
    } finally {
      globalThis.fetch = original;
      requests.length = 0;
    }
  });
});

test("the adapter reads every captured engine projection and agrees with the oracle per account", async () => {
  const { fromCalendar } = await loadComponent("src/lib/dashboard-projection.ts");
  const keyOf = new Map(JSON.parse(readFileSync(new URL("./fixtures/agreement-real/q1_quarter.json", import.meta.url), "utf8"))
    .performance.buckets.flatMap((b) => [...b.income.accounts, ...b.expenses.accounts]).map((a) => [a.name, a.account_id]));
  for (const [key, engine] of Object.entries(realPerformance)) {
    const [from, to, unit] = key.split("/");
    const read = fromCalendar(engine);
    const oracle = performance(from, to, unit);
    assert.equal(read.income.total, String(oracle.income.total), `${key} income`);
    assert.equal(read.expenses.total, String(oracle.expenses.total), `${key} expenses`);
    for (const side of ["income", "expenses"])
      for (const account of oracle[side].accounts) {
        const category = read[side].categories.find((c) => c.code === keyOf.get(account.name));
        assert.equal(category?.total, String(account.amount), `${key} ${account.name}`);
      }
  }
});

test("the adapter refuses contributions that are unsigned or don't add up, never reading them as zero", async () => {
  const { fromCalendar } = await loadComponent("src/lib/dashboard-projection.ts");
  const base = captured("q1_quarter");
  const altered = (change) => {
    const p = structuredClone(base);
    change(p.buckets[0].expenses.accounts.find((a) => a.negative_minor !== "0"));
    return p;
  };
  assert.doesNotThrow(() => fromCalendar(base));
  // The old reading: the negative part as a positive size.
  assert.throws(() => fromCalendar(altered((a) => { a.negative_minor = a.negative_minor.replace("-", ""); })), /does not equal its contributions/);
  assert.throws(() => fromCalendar(altered((a) => { a.positive_minor = String(BigInt(a.positive_minor) + 1n); })), /does not equal its contributions/);
});
