import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Synthetic Book", currency: "USD", scale: 2 };
const account = (code, name, kind, extra = {}) => ({ code, name, kind, usage: "general", currency: "", scale: 2, balance_minor: "0", native_minor: null, posting_count: 3, ...extra });
const weeks = (n) => Array.from({ length: 52 }, (_, i) => (i === 51 ? n : 0));

test("the overview shows each class, the balance check as recorded, and accounts worth a look", async () => {
  await withDOM(async ({ render }) => {
    const { AccountsOverview } = await loadComponent("src/components/workspaces/accounts-overview.tsx");
    const { accountHierarchy } = await loadComponent("src/lib/account-hierarchy.ts");
    const { classifiedAccounts } = await loadComponent("src/components/workspaces/account-tree.tsx");
    const accounts = [
      account("cash", "Operating Checking", "asset", { usage: "cash", currency: "USD" }),
      account("idle", "Studio Equipment", "asset"),
      account("drawn", "Owner Draws", "asset"),
      account("eur", "Euro Account", "asset", { currency: "EUR" }),
      account("card", "Business Card", "liability"),
      account("owner", "Owner Contributions", "equity"),
      account("sales", "Sales", "revenue"),
      account("rent", "Rent", "expense"),
    ];
    const amounts = new Map([["cash", -500n], ["idle", 18000n], ["drawn", -2000n], ["eur", 1000n], ["card", -700n], ["owner", 40000n], ["sales", 30000n], ["rent", 10000n]]);
    const history = (lastDate, n = 0) => ({ weekly: weeks(n), total: n, lastDate });
    const activity = {
      bookId: book.id, asOf: "2026-11-07", from: "2025-11-10", emptyWeekly: weeks(0),
      accounts: { cash: history("2026-11-01", 2), idle: history("2025-01-03"), drawn: history("2026-10-01", 1), eur: history("2026-10-02", 1), card: history("2026-11-02", 1), owner: history("2025-01-02"), sales: history("2026-11-03", 2), rent: history("2026-11-01", 1) },
      groups: new Map(), nativeBalances: { eur: "900" }, nativeCurrencies: { eur: "EUR" },
    };
    // Assets 67,000 = Liabilities 7,000 + Equity 40,000 + income less expenses 20,000.
    const snapshot = { assets: "67000", liabilities: "7000", equity: "60000", equityAccounts: "40000", accumulatedResult: "20000", income: "30000", expenses: "10000", result: "20000", through: "2026-11-07", rows: [], incomeRows: [], expenseRows: [] };
    const roots = classifiedAccounts(accountHierarchy(accounts));
    const opened = [], nodes = [];
    let showAll = 0;
    await render(React.createElement(AccountsOverview, {
      book, range: { from: "2026-01-01", to: "2026-11-07" }, snapshot, roots, accounts, amounts, activity, unused: 4, filtered: false,
      nodeName: (node) => node.name, accountName: (a) => a.name,
      onOpenNode: (node) => nodes.push(node.name), onOpenAccount: (code) => opened.push(code), onShowUnused: () => showAll++,
    }));
    const root = document.getElementById("root");
    const classes = [...root.querySelectorAll(".accounts-overview-class")].map((b) => b.getAttribute("aria-label"));
    assert.deepEqual(classes, ["Open Assets", "Open Liabilities", "Open Equity", "Open Income", "Open Expenses"]);
    assert.match(root.textContent, /Balanced on/);
    assert.match(root.textContent, /Income less expenses\$200\.00/);
    assert.ok(!/without a class/i.test(root.textContent), "nothing unclassified when the figures meet");
    const group = (id) => [...root.querySelectorAll(`[data-group="${id}"] .accounts-overview-fact`)].map((b) => b.textContent);
    assert.equal(group("quiet").length, 2, "Studio Equipment and Owner Contributions hold balances with no entries this period");
    // Only cash overdrawn and a card paid past what was owed; owner draws sit below zero by design.
    assert.deepEqual(group("negative").map((t) => t.replace(/[−-]?\$.*/, "")), ["Operating CheckingOverdrawn on Nov 7", "Business CardPaid past what was owed on Nov 7"]);
    assert.match(group("foreign")[0], /Euro Account.*€9\.00|Euro Account.*9\.00/);
    await React.act(async () => root.querySelector('[data-group="negative"] .accounts-overview-fact').click());
    assert.deepEqual(opened, ["cash"]);
    await React.act(async () => root.querySelector(".accounts-overview-class").click());
    assert.deepEqual(nodes, ["Assets"]);
    await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Show all accounts").click());
    assert.equal(showAll, 1);
  });
});
