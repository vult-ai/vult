import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

/* One fictional Book's year, described once. Every server response below is derived
   from it, so if two Dashboard cards disagree, the interface is at fault. */
const { V0Overview, readBookState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
const book = { id: "11111111-1111-4111-8111-111111111111", name: "Example Studio", start_date: "2024-01-01", currency: "USD", scale: 2, accounting_timezone: "UTC" };

// Calendar periods as the route returns them (projection contract 2): the unit the
// interface asks for, exclusive upper bounds, covered dates clipped to the period.
const DAY = 86400000;
const at = (s) => new Date(s + "T00:00:00Z");
const iso = (d) => d.toISOString().slice(0, 10);
function unitFor(from, to) {
  const days = (at(to) - at(from)) / DAY + 1;
  if (days <= 31) return "day";
  if (days <= 98) return "week";
  const months = (Number(to.slice(0, 4)) - Number(from.slice(0, 4))) * 12 + Number(to.slice(5, 7)) - Number(from.slice(5, 7)) + 1;
  if (months <= 14) return "month";
  if (Math.ceil(months / 3) <= 14) return "quarter";
  return "year";
}
function calendar(from, to, unit) {
  const start = (d) => unit === "day" ? d : unit === "week" ? new Date(+d - ((d.getUTCDay() + 6) % 7) * DAY)
    : new Date(Date.UTC(d.getUTCFullYear(), unit === "year" ? 0 : d.getUTCMonth() - (unit === "quarter" ? d.getUTCMonth() % 3 : 0), 1));
  const next = (d) => unit === "day" ? new Date(+d + DAY) : unit === "week" ? new Date(+d + 7 * DAY)
    : new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + (unit === "month" ? 1 : unit === "quarter" ? 3 : 12), 1));
  const end = iso(new Date(+at(to) + DAY)), out = [];
  for (let d = start(at(from)); iso(d) < end; d = next(d)) {
    const a = iso(d), b = iso(next(d));
    out.push({ from: a, to: b, covered_from: a < from ? from : a, covered_to: b > end ? end : b });
  }
  return out;
}
/** The route's calendar form from per-account amounts, one list entry per bucket. */
function calendarPerformance(from, to, unit, buckets, sides) {
  const side = (name, i) => {
    const list = sides[name].map(([code, label, values]) => {
      const v = values[i];
      return { account_id: code, name: label, amount_minor: String(v), positive_minor: String(v > 0 ? v : 0), negative_minor: String(v < 0 ? v : 0), posting_count: 1 };
    });
    const sum = (k) => String(list.reduce((n, a) => n + Number(a[k]), 0));
    return { total_minor: sum("amount_minor"), positive_minor: sum("positive_minor"), negative_minor: sum("negative_minor"), accounts: list };
  };
  const out = buckets.map((b, i) => ({ ...b, income: side("income", i), expenses: side("expenses", i) }));
  const total = (k) => String(out.reduce((n, b) => n + Number(b[k].total_minor), 0));
  return { status: "recorded", period: { from, to }, unit, timezone: "UTC", income_minor: total("income"), expenses_minor: total("expenses"), buckets: out };
}
const accounts = {
  income: [["sales", "Sales", 900000], ["consulting", "Consulting", 250000]],
  expenses: [["rent", "Rent", 300000], ["software", "Software", 40000], ["travel", "Travel", 90000]],
};
// One slice where a refund outweighs that slice's software spend: a negative value.
function amount(side, code, base, i) {
  if (side === "expenses" && code === "software" && i === 5) return -125000;
  return Math.round(base * (1 + ((i * 7 + code.length) % 5) / 10)) / 2;
}
/* Six months written out by hand, with totals worked out by hand below (not by the code
   under test). March holds a software refund larger than that month's software spend. */
const handWritten = {
  from: "2025-01-01", to: "2025-06-30",
  months: ["01", "02", "03", "04", "05", "06"],
  income: { sales: [100000, 120000, 90000, 110000, 100000, 80000] },
  expenses: { rent: [50000, 50000, 50000, 50000, 50000, 50000], software: [10000, 10000, -30000, 10000, 0, 10000] },
};
// By hand: income 6,000.00; expenses 3,000.00 + 100.00 = 3,100.00; net 2,900.00.
// March: income 900.00; expenses 500.00 - 300.00 = 200.00; net 700.00.
function handWrittenPerformance() {
  const h = handWritten, buckets = calendar(h.from, h.to, "month");
  const named = (list) => Object.entries(list).map(([code, values]) => [code, code[0].toUpperCase() + code.slice(1), values]);
  return calendarPerformance(h.from, h.to, "month", buckets, { income: named(h.income), expenses: named(h.expenses) });
}
function performanceFor(from, to) {
  if (from === handWritten.from && to === handWritten.to) return handWrittenPerformance();
  const unit = unitFor(from, to), buckets = calendar(from, to, unit);
  const side = (name) => accounts[name].map(([code, label, base]) => [code, label, buckets.map((_, i) => amount(name, code, base, i))]);
  return calendarPerformance(from, to, unit, buckets, { income: side("income"), expenses: side("expenses") });
}
const net = (p) => Number(p.income_minor) - Number(p.expenses_minor);
// Position on a date: cash and debts move with the date, so "today" differs from the period end.
const position = (to) => {
  const k = Number(to.slice(0, 4)) - 2024;
  const card = 410000 + 37000 * k, loan = 1500000 - 120000 * k, cash = 2300000 + 450000 * k;
  const assets = cash + 900000;
  return { cash, card, loan, assets, liabilities: card + loan, equity: assets - card - loan };
};
function snapshot(from, to) {
  const p = performanceFor(from, to), pos = position(to);
  const period = { from, to };
  const previousYear = (d) => `${Number(d.slice(0, 4)) - 1}${d.slice(4)}`;
  const previous = { from: previousYear(from), to: previousYear(to) };
  const prior = performanceFor(previous.from, previous.to);
  return {
    schema_version: 1, book_id: book.id, period, currency: book.currency, scale: book.scale,
    cash_minor: String(pos.cash), cash_status: "partial",
    cash_accounts: [{ code: "asset:checking", name: "Checking", carrying_minor: String(pos.cash) }],
    trends: { current: [], previous: [] },
    prior_year: { income: prior.income_minor, expenses: prior.expenses_minor, result: String(net(prior)), period: previous },
    financial: {
      assets: String(pos.assets), liabilities: String(pos.liabilities), equity: String(pos.equity), equityAccounts: String(pos.equity), accumulatedResult: "0",
      income: p.income_minor, expenses: p.expenses_minor, result: String(net(p)), through: to,
      rows: [
        { code: "asset:checking", name: "Checking", kind: "asset", amount: String(pos.cash) },
        { code: "asset:equipment", name: "Equipment", kind: "asset", amount: "900000" },
        { code: "liability:card", name: "Business card", kind: "liability", amount: String(pos.card) },
        { code: "liability:loan", name: "Bank loan", kind: "liability", amount: String(pos.loan) },
      ],
      incomeRows: [], expenseRows: [],
    },
    overviewComparison: { current: [], previous: [], range: { from, to }, previousRange: { from, to } },
    performance: p,
  };
}
const state = {
  book, accounts: [], cash_accounts: [], transactions: [], currencies: [{ code: "USD", scale: 2, name: "US Dollar" }],
  state_revision: "a".repeat(64),
};
const json = (body) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
function server(window, requests) {
  return async (url) => {
    const u = new URL(String(url), window.location.href), q = u.searchParams;
    requests.push(u.pathname);
    assert.equal(u.pathname, "/api/v1/state");
    assert.equal(q.get("view"), "dashboard");
    const from = q.get("dashboard_from"), to = q.get("dashboard_to");
    assert.equal(q.get("dashboard_unit"), unitFor(from, to));
    return json({ ...state, dashboard_snapshot: snapshot(from, to) });
  };
}
const read = (from, to) => readBookState(book.id, {}, book.accounting_timezone, { view: "dashboard", range: { from, to } });
const mount = (render, snapshotState, extra = {}) => render(React.createElement(V0Overview, {
  page: "dashboard", state: snapshotState, book, onNavigate() {}, onShowTransactions() {}, onInspect() {}, ...extra,
}));

const panel = (name) => [...document.querySelectorAll(".dash-panel")].find((p) => p.getAttribute("aria-label") === name);
const headline = (name) => panel(name)?.querySelector(".vult-headline .rolling-number-text")?.textContent;
const stat = (name, label) => [...panel(name).querySelectorAll(".dash-stat")]
  .find((s) => s.querySelector(".dash-stat-label").textContent.trim() === label)
  ?.querySelector(".dash-figure .rolling-number-text")?.textContent;
const settle = () => React.act(() => new Promise((done) => setTimeout(done, 30)));

for (const [label, from, to] of [
  ["one month", "2025-03-01", "2025-03-31"],
  ["a full year with a refund", "2025-01-01", "2025-12-31"],
  ["a multi-year history", "2024-01-01", "2026-10-31"],
]) {
  test(`every Dashboard card reports the same figures: ${label}`, async () => {
    await withDOM(async ({ render, window }) => {
      window.history.replaceState(null, "", `/dashboard?from=${from}&to=${to}`);
      const original = globalThis.fetch;
      const requests = [];
      globalThis.fetch = server(window, requests);
      try {
        await mount(render, await read(from, to));
        for (let i = 0; i < 10 && !headline("Income and expenses")?.match(/\d/); i++) await settle();
        const summaryNet = stat("Summary", "Net result");
        assert.match(summaryNet ?? "", /\d/, "the summary loaded");
        assert.deepEqual(requests, ["/api/v1/state"]);
        assert.equal(headline("Income and expenses"), summaryNet, "chart net equals the income statement's net result");
        assert.equal(headline("Where the money went"), stat("Summary", "Expenses"), "spending total equals expenses");
        assert.equal(headline("What you owe"), stat("Balance sheet", "Liabilities"), "what you owe equals liabilities on the same date");
        assert.equal(headline("Cash by account"), stat("Summary", "Cash"), "cash card equals the summary's cash");
        // Each card's other view agrees with the same totals.
        const flip = async (card, mode) => {
          await React.act(async () => [...panel(card).querySelectorAll(".dash-mode-option")].find((b) => b.textContent === mode).click());
        };
        await flip("Where the money went", "In");
        assert.equal(headline("Where the money came from"), stat("Summary", "Income"), "income total equals income");
        await flip("What you owe", "Owed to you");
        assert.ok(panel("Owed to you"), "the card's other view is what others owe");
        const expected = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(position(to).cash / 100);
        assert.equal(stat("Summary", "Cash"), expected, "cash is the balance on the period's last date, not today's");
      } finally {
        globalThis.fetch = original;
      }
    });
  });
}

test("hand-worked amounts: refunds stay signed in the chart, its table and every card", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/dashboard?from=${handWritten.from}&to=${handWritten.to}`);
    const original = globalThis.fetch;
    const requests = [];
    globalThis.fetch = server(window, requests);
    try {
      await mount(render, await read(handWritten.from, handWritten.to));
      for (let i = 0; i < 10 && !headline("Income and expenses")?.match(/\d/); i++) await settle();
      assert.equal(headline("Income and expenses"), "$2,900.00");
      assert.equal(stat("Summary", "Net result"), "$2,900.00");
      assert.equal(stat("Summary", "Expenses"), "$3,100.00");
      assert.equal(headline("Where the money went"), "$3,100.00");
      // The refund is drawn below zero, not dropped, and the page says what that means.
      assert.equal(panel("Income and expenses").querySelectorAll(".dash-segment-negative").length, 1);
      // The legend keys it, and explains it on demand rather than in a row of its own.
      const key = panel("Income and expenses").querySelector(".dash-legend-help");
      assert.match(key?.textContent ?? "", /below zero/);
      await React.act(async () => key.click());
      assert.match(document.querySelector('[role="tooltip"]')?.textContent ?? "", /Refunds and credits/);
      await React.act(async () => key.click());
      // Hovering previews a period in its tooltip only; a click chooses it for the headline.
      const marchBar = panel("Income and expenses").querySelectorAll(".dash-group")[2];
      await React.act(async () => marchBar.dispatchEvent(new window.MouseEvent("mouseover", { bubbles: true })));
      assert.equal(headline("Income and expenses"), "$2,900.00", "hover leaves the headline alone");
      await React.act(async () => marchBar.click());
      await settle();
      assert.equal(marchBar.getAttribute("aria-pressed"), "true");
      assert.equal(headline("Income and expenses"), "$700.00", "a click shows March's net");
      await React.act(async () => marchBar.click());
      await settle();
      assert.equal(headline("Income and expenses"), "$2,900.00", "a second click returns to the whole range");
      const softwareRow = [...panel("Where the money went").querySelectorAll(".dash-row")].find((r) => r.textContent.includes("Software"));
      await React.act(async () => softwareRow.dispatchEvent(new window.MouseEvent("mouseover", { bubbles: true })));
      assert.equal(headline("Where the money went"), "$3,100.00", "hover leaves the spending total alone");
      await React.act(async () => softwareRow.click());
      await settle();
      assert.equal(headline("Where the money went"), "$100.00", "a click shows Software after its refund");
      const showAll = [...panel("Where the money went").querySelectorAll("button")].find((b) => b.textContent === "Show all");
      await React.act(async () => showAll.click());
      await settle();
      assert.equal(headline("Where the money went"), "$3,100.00");
      // The table gives the exact amounts per month.
      const toggle = [...panel("Income and expenses").querySelectorAll("button")].find((b) => b.textContent === "Show table");
      await React.act(async () => toggle.click());
      const rows = [...panel("Income and expenses").querySelectorAll(".dash-table tbody tr")].map((r) => [...r.children].map((c) => c.textContent));
      assert.equal(rows.length, 6);
      assert.deepEqual(rows[2], ["March 2025", "$900.00", "$200.00", "$700.00"]);
      // Each period opens to its accounts with exact amounts; the refund stays negative.
      const march = panel("Income and expenses").querySelector('button[aria-label="Show accounts for March 2025"]');
      await React.act(async () => march.click());
      assert.equal(march.getAttribute("aria-expanded"), "true");
      const accounts = [...panel("Income and expenses").querySelectorAll(".dash-table-account")].map((r) => [...r.children].map((c) => c.textContent));
      assert.deepEqual(accounts, [["Sales", "$900.00", "", ""], ["Rent", "", "$500.00", ""], ["Software", "", "−$300.00", ""]]);
      const foot = [...panel("Income and expenses").querySelectorAll(".dash-table tfoot td")].map((c) => c.textContent);
      assert.deepEqual(foot, ["$6,000.00", "$3,100.00", "$2,900.00"]);
    } finally {
      globalThis.fetch = original;
    }
  });
});


test("a delayed period change keeps every figure and the cash date on the confirmed snapshot", async () => {
  await withDOM(async ({ render, window }) => {
    const from = "2025-01-01", to = "2025-06-30";
    window.history.replaceState(null, "", `/dashboard?from=${from}&to=${to}`);
    const original = globalThis.fetch, requests = [];
    const respond = server(window, requests);
    globalThis.fetch = respond;
    try {
      const opened = [];
      const props = { onShowTransactions: (range) => opened.push(range) };
      const first = await read(from, to);
      await mount(render, first, props);
      const cash = stat("Summary", "Cash");
      const caption = () => panel("Summary").querySelector('[data-series="cash"] .dash-stat-foot').textContent;
      const net = stat("Summary", "Net result");
      assert.match(caption(), /Jun 30/);
      let finish;
      globalThis.fetch = (url) => new Promise((resolve) => { finish = () => resolve(respond(url)); });
      await React.act(async () => {
        window.history.pushState(null, "", "/dashboard?from=2026-01-01&to=2026-03-31");
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(typeof finish, "function");
      assert.equal(document.querySelector(".v0-overview").getAttribute("aria-busy"), "true");
      assert.equal(stat("Summary", "Cash"), cash);
      assert.equal(stat("Summary", "Net result"), net);
      assert.match(caption(), /Jun 30/);
      const refreshed = {
        ...first,
        dashboard_snapshot: {
          ...first.dashboard_snapshot, version: "b".repeat(64),
          cash_accounts: first.dashboard_snapshot.cash_accounts.map((account) => ({ ...account, name: "Renamed cash" })),
        },
      };
      await mount(render, refreshed, props);
      assert.equal(document.querySelector(".v0-overview").getAttribute("aria-busy"), "true");
      assert.match(panel("Cash by account").textContent, /Renamed cash/);
      assert.match(caption(), /Jun 30/);
      await React.act(async () => panel("Cash by account").querySelector(".dash-row-open").click());
      assert.deepEqual(opened.at(-1), { from: "", to, account: "asset:checking" });
      await React.act(async () => finish());
      assert.match(caption(), /Mar 31/);
      assert.notEqual(stat("Summary", "Cash"), cash);
      assert.notEqual(stat("Summary", "Net result"), net);
      assert.equal(headline("Cash by account"), stat("Summary", "Cash"));
      await React.act(async () => panel("What you owe").querySelector(".dash-row-open").click());
      assert.deepEqual(opened.at(-1), { from: "", to: "2026-03-31", account: "liability:loan" });
      assert.deepEqual(requests, ["/api/v1/state", "/api/v1/state"]);
    } finally {
      globalThis.fetch = original;
    }
  });
});
