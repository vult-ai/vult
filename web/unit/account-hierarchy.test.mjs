import { accountReadResponse } from "./helpers/account-reads.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "hierarchy-book",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  start_date: "2025-01-01",
  accounting_today: "2026-10-25",
  accounting_timezone: "UTC",
};
const range = { from: "2026-01-01", to: "2026-10-25" };
// Parents are recorded links, never derived from names.
const position = (code, name, kind = "expense", posting_count = 1, parent_id) => ({
  code,
  name,
  ...(parent_id ? { parent_id } : {}),
  kind,
  usage: "general",
  currency: "",
  scale: 2,
  balance_minor: "0",
  native_minor: "",
  posting_count,
});
const accounts = [
  position("soft", "Software"),
  position("host", "Cloud hosting", "expense", 1, "soft"),
  position("tools", "Tools", "expense", 1, "soft"),
  position("design", "Design", "expense", 1, "tools"),
  position("cash", "Operating", "asset"),
  position("borrowings", "Borrowings", "liability", 0),
  position("loan", "Bank loan", "liability", 1, "borrowings"),
  position("software-assets", "Software", "asset", 0),
  position("capital", "Capitalized license", "asset", 0, "software-assets"),
  // A parent across account classes is ignored; a name with a colon is just a name.
  position("crossed", "Software:Not a child", "asset", 0, "soft"),
];
const journal = [];
function entry(id, date, postings) {
  for (const [account_code, side, amount_minor] of postings)
    journal.push({
      transaction_id: id,
      book_id: book.id,
      currency: "USD",
      scale: "2",
      economic_date: date,
      line_id: `${id}-${account_code}`,
      account_code,
      side,
      amount_minor: String(amount_minor),
      native_currency: "",
      native_amount_minor: "",
    });
}
entry("loan", "2025-01-01", [
  ["cash", "debit", 10000],
  ["loan", "credit", 10000],
]);
entry("prior", "2025-12-01", [
  ["host", "debit", 700],
  ["cash", "credit", 700],
]);
entry("direct", "2026-02-01", [
  ["soft", "debit", 1000],
  ["cash", "credit", 1000],
]);
entry("hosting", "2026-03-01", [
  ["host", "debit", 2000],
  ["cash", "credit", 2000],
]);
entry("design", "2026-04-01", [
  ["design", "debit", 3000],
  ["cash", "credit", 3000],
]);
entry("tools", "2026-05-01", [
  ["tools", "debit", 500],
  ["cash", "credit", 500],
]);
entry("correction", "2026-06-01", [
  ["cash", "debit", 200],
  ["host", "credit", 200],
]);
entry("split", "2026-07-01", [
  ["host", "debit", 100],
  ["design", "debit", 200],
  ["cash", "credit", 300],
]);
entry("future", "2026-12-01", [
  ["host", "debit", 9999],
  ["cash", "credit", 9999],
]);
const headers = Object.keys(journal[0]);
const csv =
  headers.join(",") +
  "\r\n" +
  journal.map((row) => headers.map((key) => row[key]).join(",")).join("\r\n") +
  "\r\n";
const state = {
  accounts: accounts.map(({ code, name, kind }) => ({ code, name, kind })),
  account_balances: accounts,
  transactions: [],
};

test("parent totals include direct postings and all descendants once, with period flows separate from dated positions", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const {
    accountHierarchy,
    accountAmounts,
    nodeAmount,
    accountBasis,
    hierarchyRows,
  } = await loadComponent("src/lib/account-hierarchy.ts");
  const snapshot = financialSnapshot(book, state, journal, range),
    amounts = accountAmounts(snapshot, accounts),
    roots = accountHierarchy(accounts);
  const software = roots.find(
    (node) => node.name === "Software" && node.kind === "expense",
  );
  const capital = roots.find(
    (node) => node.name === "Software" && node.kind === "asset",
  );
  assert.deepEqual(
    new Set(software.codes),
    new Set(["soft", "host", "tools", "design"]),
  );
  assert.equal(
    nodeAmount(software, amounts),
    6600n,
    "excludes prior-year and future expenses, includes reversals and parent/grandchild postings",
  );
  assert.equal(
    amounts.get("cash"),
    2700n,
    "cash includes earlier balances through the cutoff",
  );
  assert.equal(amounts.get("loan"), 10000n);
  assert.equal(
    nodeAmount(capital, amounts),
    0n,
    "capitalized software is a separate asset hierarchy",
  );
  assert.equal(accountBasis(software.kind), "period");
  assert.equal(accountBasis(capital.kind), "balance");
  const tools = software.children.find((node) => node.name === "Tools");
  const rows = hierarchyRows(
    roots,
    new Set([software.key, tools.key]),
    (a, b) => a.name.localeCompare(b.name),
  );
  assert.equal(
    rows.filter((row) => row.account?.code === "soft" && row.direct).length,
    1,
  );
  assert.equal(
    rows.filter((row) => row.account?.code === "tools" && row.direct).length,
    1,
  );
  assert.equal(rows.filter((row) => row.account?.code === "design").length, 1);
  assert.throws(
    () => accountHierarchy([...accounts, accounts[0]]),
    /duplicate/,
  );
  const crossed = roots.find((node) => node.own[0]?.code === "crossed");
  assert.equal(crossed?.name, "Software:Not a child", "a colon in a name is part of the name, and a cross-class link is ignored");
  assert.ok(!software.codes.includes("crossed"));
});

test("group activity deduplicates split transactions and native balances honor the same cutoff", async () => {
  const { parseJournalAccountActivity } = await loadComponent(
    "src/lib/account-hierarchy-activity.ts",
  );
  const { accountHierarchy, nodeActivity, nodeWeeklyActivity } =
    await loadComponent("src/lib/account-hierarchy.ts");
  const activity = parseJournalAccountActivity(csv, book, range.to),
    software = accountHierarchy(accounts).find(
      (node) => node.name === "Software" && node.kind === "expense",
    );
  assert.equal(
    nodeActivity(software, activity).count,
    7,
    "split across sibling accounts remains one transaction",
  );
  assert.equal(nodeActivity(software, activity).lastDate, "2026-07-01");
  assert.equal(
    nodeWeeklyActivity(software, activity).weekly.reduce(
      (sum, value) => sum + (value || 0),
      0,
    ),
    7,
    "the detail graph counts the split transaction only once",
  );
  const native = journal.map((row) => ({
    ...row,
    native_currency: row.account_code === "cash" ? "EUR" : "",
    native_amount_minor: row.account_code === "cash" ? row.amount_minor : "",
  }));
  const text =
    headers.join(",") +
    "\r\n" +
    native.map((row) => headers.map((key) => row[key]).join(",")).join("\r\n");
  assert.equal(
    parseJournalAccountActivity(text, book, range.to).nativeBalances.cash,
    "2700",
  );
  assert.throws(
    () =>
      parseJournalAccountActivity(
        text + "\r\n" + headers.map((key) => native[0][key]).join(","),
        book,
        range.to,
      ),
    /native balances/,
  );
});

const settle = () =>
  React.act(async () => new Promise((resolve) => setTimeout(resolve, 30)));
const click = (element) => React.act(async () => element.click());
const control = (text) =>
  [...document.querySelectorAll("button")].find(
    (button) =>
      button.getAttribute("aria-label") === text || button.textContent === text,
  );
const group = (name) =>
  [...document.querySelectorAll("[data-account-group]")].find(
    (row) => row.querySelector(".account-tree-name").textContent === name,
  );

test("the compact tree selects a main account workspace with recorded activity and independent expansion", async () => {
  await withDOM(async ({ render, window }) => {
    const bounds = window.HTMLElement.prototype.getBoundingClientRect;
    window.HTMLElement.prototype.getBoundingClientRect = function () {
      const rect = bounds.call(this);
      return this.classList.contains("accounts-inspector-layout")
        ? { ...rect, width: 1200 }
        : rect;
    };
    const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch,
      calls = [],
      opened = [];
    globalThis.fetch = async (url, init) => {
      calls.push({ url: String(url), method: init?.method || "GET" });
      const scoped = accountReadResponse(url, csv, book, accounts);
      if (scoped) return scoped;
      return new Response(
        String(url).includes("transactions.csv")
          ? "transaction_id,book_id,kind,reversed_by\r\n"
          : csv,
        { headers: { "Content-Type": "text/csv" } },
      );
    };
    const source = JSON.stringify(accounts);
    try {
      const totals = {
        snapshot: financialSnapshot(book, state, journal, range),
        range,
        period: "year-to-date",
        pending: false,
        error: "",
        retry: () => {},
        select: () => {},
        selectRange: () => {},
        state,
      };
      await render(
        React.createElement(AccountActivityList, {
          accounts,
          book,
          refreshKey: "hierarchy",
          totals,
          onOpen: (range) => opened.push(range),
        }),
      );
      await settle();
      assert.deepEqual(
        [
          ...document.querySelectorAll(
            "[data-account-group] .account-tree-name",
          ),
        ].map((e) => e.textContent),
        ["Assets", "Liabilities", "Borrowings", "Expenses", "Software"],
      );
      assert.ok(
        document.querySelector('[data-account-code="cash"]'),
        "the chart opens with accounts visible",
      );
      assert.equal(document.querySelector(".account-tree-pane table"), null);
      assert.equal(document.querySelector(".account-tree-columns"), null);
      assert.equal(
        document.querySelector(
          ".account-tree-pane .account-activity-sparkline",
        ),
        null,
      );
      assert.match(
        document.querySelector(".account-tree-convention").textContent,
        /^Activity .* · Balances as of Oct 25/,
      );
      assert.match(
        document.querySelector('[data-testid="account-inspector"]').textContent,
        /Account classes[\s\S]*Balance check/,
        "before selection the right workspace shows the overview",
      );
      assert.ok(
        document.querySelector(
          ".collection-inspector > .collection-inspector-collection .accounts-activity-page",
        ),
        "controls and tree share the inset collection surface",
      );

      await click(group("Assets").querySelector(".account-tree-name"));
      await settle();
      assert.equal(
        group("Assets").getAttribute("aria-expanded"),
        "true",
        "clicking a parent opens its details without expanding it",
      );
      assert.match(
        document.querySelector(".account-detail-header").textContent,
        /Assets.*Balance.*October 25, 2026/s,
      );
      assert.equal(
        document.querySelector(".account-group-activity-total").textContent,
        "$27.00",
      );
      await click(group("Assets").querySelector(".account-tree-chevron"));
      assert.equal(group("Assets").getAttribute("aria-expanded"), "false");
      assert.match(
        document.querySelector(".account-detail-header").textContent,
        /Assets/,
        "the chevron expands without changing the selected details",
      );
      await click(group("Assets").querySelector(".account-tree-chevron"));
      assert.equal(group("Assets").getAttribute("aria-expanded"), "true");
      assert.match(
        document.querySelector(".account-detail-header").textContent,
        /Assets/,
      );
      const chart = document.querySelector(".account-tree");
      chart.scrollTop = 71;
      await click(
        document.querySelector('[aria-label="Close account details"]'),
      );
      assert.equal(document.querySelector(".account-tree"), chart);
      assert.equal(
        chart.scrollTop,
        71,
        "clearing selection preserves the tree scroll",
      );
      assert.equal(group("Assets").getAttribute("aria-expanded"), "true");

      await click(control("Expand all"));
      assert.equal(
        document.querySelector("[role=dialog]"),
        null,
        "expansion does not open account details",
      );
      assert.equal(document.querySelector(".account-tree-basis"), null);
      assert.equal(
        document.querySelectorAll(".account-tree-currency").length,
        1,
      );
      assert.match(
        document.querySelector(".account-tree-currency").textContent,
        /USD/,
      );
      const software = group("Software");
      assert.equal(
        software.querySelector(".account-tree-amount").textContent,
        "$66.00",
      );
      assert.equal(
        document.querySelector('[data-account-code="soft"] .account-tree-name')
          .textContent,
        "Direct postings",
      );
      assert.equal(
        document.querySelector(
          '[data-account-code="soft"] .account-tree-amount',
        ).textContent,
        "$10.00",
      );
      assert.equal(
        document.querySelector(
          '[data-account-code="host"] .account-tree-amount',
        ).textContent,
        "$19.00",
      );
      assert.equal(
        document.querySelector(
          '[data-account-code="cash"] .account-tree-amount',
        ).textContent,
        "$27.00",
      );
      assert.match(
        document
          .querySelector('[data-account-code="cash"]')
          .getAttribute("aria-label"),
        /balance as of/,
      );
      assert.match(
        document
          .querySelector('[data-account-code="host"]')
          .getAttribute("aria-label"),
        /period total/,
      );
      assert.equal(
        new Set(
          [...document.querySelectorAll("[data-account-code]")].map(
            (row) => row.dataset.accountCode,
          ),
        ).size,
        6,
      );
      await click(software.querySelector(".account-tree-name"));
      await settle();
      assert.equal(
        group("Software").getAttribute("aria-expanded"),
        "true",
        "clicking a nested parent opens details without collapsing it",
      );
      const detail = document.querySelector(
        '[data-testid="account-inspector"]',
      );
      assert.ok(detail);
      assert.match(
        detail.querySelector(".account-detail-header").textContent,
        /Period total.*January 1.*October 25.*2026.*USD/,
      );
      assert.equal(
        detail.querySelector(".account-group-activity-total").textContent,
        "$66.00",
      );
      assert.equal(
        detail.querySelectorAll("table").length,
        1,
        "the selected account has a recorded-activity table in the main workspace",
      );
      assert.equal(document.querySelector("[role=dialog]"), null);
      assert.equal(detail.querySelector(".account-activity-sparkline"), null);
      assert.equal(
        document.querySelectorAll(
          ".account-tree-pane .account-activity-sparkline",
        ).length,
        0,
      );
      await React.act(async () => {
        document.querySelector(".accounts-inspector-frame").dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "Escape",
            bubbles: true,
          }),
        );
      });
      assert.match(
        document.querySelector('[data-testid="account-inspector"]').textContent,
        /Account classes[\s\S]*Balance check/,
        "clearing selection returns the right workspace to the overview",
      );
      assert.equal(
        document.querySelector('[data-testid="account-details"]'),
        null,
      );
      await click(
        document.querySelector(
          '[data-account-code="cash"] .account-tree-amount',
        ),
      );
      await settle();
      assert.match(
        document.querySelector(".account-detail-header").textContent,
        /Balance.*October 25, 2026/,
      );
      assert.equal(
        document.querySelector(".account-group-activity-total").textContent,
        "$27.00",
      );
      await click(document.querySelector('[aria-label="Account actions"]'));
      await click(
        [...document.querySelectorAll('[role="menuitem"]')].find((item) =>
          item.textContent.includes("View account activity"),
        ),
      );
      assert.deepEqual(opened.pop(), {
        from: "2025-01-01",
        to: range.to,
        account: "cash",
      });
      await click(
        document.querySelector(
          '[data-account-code="host"] .account-tree-amount',
        ),
      );
      await settle();
      await click(document.querySelector('[aria-label="Account actions"]'));
      await click(
        [...document.querySelectorAll('[role="menuitem"]')].find((item) =>
          item.textContent.includes("View account activity"),
        ),
      );
      assert.deepEqual(opened.pop(), { ...range, account: "host" });
      await click(control("Collapse all"));
      assert.equal(document.querySelectorAll(".account-tree-row").length, 3);
      await click(document.querySelector(".account-tree-options-trigger"));
      await click(control("Reorder accounts"));
      assert.equal(
        document.querySelectorAll(".account-tree .account-row-grip").length,
        6,
      );
      await click(control("Done reordering"));
      assert.equal(document.querySelectorAll(".account-tree-row").length, 3);
      assert.equal(JSON.stringify(accounts), source);
      assert.ok(calls.every((call) => call.method === "GET"));
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("filtered totals disclose their scope and a changed ledger cannot produce a false breakdown", async () => {
  await withDOM(async ({ render }) => {
    const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch;
    let currentCSV = csv;
    globalThis.fetch = async (url) =>
      accountReadResponse(url, currentCSV, book, accounts) || new Response(
        String(url).includes("transactions.csv")
          ? "transaction_id,book_id,kind,reversed_by\r\n"
          : currentCSV,
        { headers: { "Content-Type": "text/csv" } },
      );
    try {
      let refreshes = 0;
      const totals = {
        snapshot: financialSnapshot(book, state, journal, range),
        range,
        period: "year-to-date",
        pending: false,
        error: "",
        retry: () => refreshes++,
        select: () => {},
        selectRange: () => {},
        state,
      };
      const props = {
        accounts,
        book,
        refreshKey: "partial",
        totals,
        onOpen: () => {},
        filterAccount: (account) => account.code !== "host",
      };
      await render(React.createElement(AccountActivityList, props));
      await React.act(
        async () => new Promise((resolve) => setTimeout(resolve, 30)),
      );
      await click(control("Expand all"));
      const software = () =>
        [...document.querySelectorAll("[data-account-group]")].find(
          (row) =>
            row.querySelector(".account-tree-name").textContent === "Software",
        );
      assert.equal(
        software().querySelector(".account-tree-amount").textContent,
        "$47.00",
      );
      assert.match(software().getAttribute("aria-label"), /filtered/i);
      await React.act(async () =>
        software().querySelector(".account-tree-amount").click(),
      );
      await React.act(
        async () => new Promise((resolve) => setTimeout(resolve, 30)),
      );
      assert.match(
        document.querySelector('[role="dialog"]').textContent,
        /Filtered account scope/,
      );
      assert.equal(
        document.querySelectorAll('[role="dialog"] tbody tr').length,
        4,
      );
      await render(null);
      const changed = journal.map((row) =>
        row.transaction_id === "direct"
          ? { ...row, amount_minor: "1100" }
          : row,
      );
      currentCSV =
        headers.join(",") +
        "\r\n" +
        changed
          .map((row) => headers.map((key) => row[key]).join(","))
          .join("\r\n");
      await render(React.createElement(AccountActivityList, props));
      await click(control("Expand all"));
      await React.act(async () =>
        software().querySelector(".account-tree-amount").click(),
      );
      await React.act(
        async () => new Promise((resolve) => setTimeout(resolve, 30)),
      );
      assert.equal(
        [...document.querySelectorAll('[role="dialog"] tbody tr')].filter(
          (row) => row.querySelectorAll("td,th").length > 1,
        ).length,
        0,
        "mismatched source evidence is never presented as a reconciled explanation",
      );
      assert.ok(
        document.querySelector(
          '[role="dialog"] .error-notice-copy[role="status"]',
        ),
      );
      const retry = [
        ...document.querySelectorAll('[role="dialog"] button'),
      ].find((button) => button.textContent === "Try again");
      assert.ok(retry);
      await React.act(async () => retry.click());
      assert.equal(
        refreshes,
        1,
        "retry also refreshes the displayed canonical projection",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("small displays open the same reconciled account detail separately", async () => {
  await withDOM(async ({ render, window }) => {
    window.matchMedia = () => ({
      matches: false,
      addEventListener() {},
      removeEventListener() {},
    });
    const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch;
    globalThis.fetch = async (url) =>
      accountReadResponse(url, csv, book, accounts) || new Response(
        String(url).includes("transactions.csv")
          ? "transaction_id,book_id,kind,reversed_by\r\n"
          : csv,
        { headers: { "Content-Type": "text/csv" } },
      );
    try {
      const totals = {
        snapshot: financialSnapshot(book, state, journal, range),
        range,
        period: "year-to-date",
        pending: false,
        error: "",
        retry: () => {},
        select: () => {},
        selectRange: () => {},
        state,
      };
      await render(
        React.createElement(AccountActivityList, {
          accounts,
          book,
          refreshKey: "phone",
          totals,
        }),
      );
      await click(control("Expand all"));
      await click(
        document.querySelector(
          '[data-account-code="host"] .account-tree-amount',
        ),
      );
      await settle();
      assert.equal(document.querySelectorAll('[role="dialog"]').length, 1);
      assert.equal(
        document.querySelector('[data-testid="account-details"]'),
        null,
      );
      assert.equal(
        document.querySelector('[role="dialog"] .account-group-activity-total')
          .textContent,
        "$19.00",
      );
      assert.ok(
        document.querySelector('[role="dialog"] .account-activity-sparkline'),
      );
      assert.equal(
        document.querySelector('[role="dialog"] svg title'),
        null,
        "details do not reintroduce the native paragraph tooltip",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("tree identity help reveals only clipped names and references without selecting or expanding", async () => {
  await withDOM(async ({ render, window }) => {
    const { AccountTree } = await loadComponent(
      "src/components/workspaces/account-tree.tsx",
    );
    const { accountHierarchy } = await loadComponent(
      "src/lib/account-hierarchy.ts",
    );
    const items = [
      position("bank", "Checking", "asset"),
      position("equipment", "Long-term office equipment", "asset", 0),
      position("device", "Phones", "asset", 1, "equipment"),
    ];
    const roots = accountHierarchy(items);
    const parent = roots.find((node) => node.children.length);
    const opened = [],
      expanded = [];
    const viewProps = {
      roots,
      book,
      amounts: new Map([
        ["bank", 1200n],
        ["device", 200n],
      ]),
      expanded: new Set([parent.key]),
      onExpanded: (keys) => expanded.push(keys),
      compare: (a, b) => a.name.localeCompare(b.name),
      nodeName: (node) => node.name,
      accountName: (account) => account.name,
      accountSuffix: (account) => (account.code === "bank" ? "•• 0042" : ""),
      icon: () => null,
      customize: () => null,
      grip: () => null,
      partial: () => false,
      onOpen: (node) => opened.push(node.key),
      contextId: "tree-context",
    };
    await render(React.createElement(AccountTree, viewProps));
    const bank = document.querySelector('[data-account-code="bank"]');
    const label = bank.querySelector(".account-tree-label");
    let clipped = false;
    Object.defineProperties(label, {
      clientWidth: { configurable: true, get: () => 160 },
      scrollWidth: { configurable: true, get: () => (clipped ? 260 : 160) },
    });
    const enter = async (element) => {
      await React.act(async () =>
        element.dispatchEvent(
          new window.MouseEvent("mouseover", { bubbles: true }),
        ),
      );
      await settle();
    };
    const leave = async (element) => {
      await React.act(async () =>
        element.dispatchEvent(
          new window.MouseEvent("mouseout", {
            bubbles: true,
            relatedTarget: document.body,
          }),
        ),
      );
      await settle();
    };
    await enter(label);
    assert.equal(
      document.querySelector('[role="tooltip"]'),
      null,
      "unclipped labels do not add tooltip noise",
    );
    await leave(label);
    clipped = true;
    await enter(label);
    assert.equal(
      document.querySelector('[role="tooltip"]').textContent,
      "Checking •• 0042",
    );
    assert.equal(opened.length, 0);
    assert.equal(expanded.length, 0);
    assert.equal(
      label.querySelector("[title]"),
      null,
      "no duplicate native title bubble",
    );
    await leave(label);
    assert.equal(document.querySelector('[role="tooltip"]'), null);
    const bankAction = bank.querySelector(".account-tree-amount");
    await React.act(async () => bankAction.focus());
    await settle();
    assert.equal(
      document.querySelector('[role="tooltip"]').textContent,
      "Checking •• 0042",
      "keyboard focus exposes the same complete identity",
    );
    await React.act(async () => bankAction.blur());
    await settle();
    assert.equal(document.querySelector('[role="tooltip"]'), null);
    const parentRow = document.querySelector(".account-tree-parent");
    const parentLabel = parentRow.querySelector(".account-tree-label");
    Object.defineProperties(parentLabel, {
      clientWidth: { configurable: true, value: 80 },
      scrollWidth: { configurable: true, value: 240 },
    });
    await enter(parentLabel);
    assert.equal(
      document.querySelector('[role="tooltip"]').textContent,
      "Long-term office equipment",
    );
    await leave(parentLabel);
    await click(parentRow.querySelector(".account-tree-chevron"));
    assert.equal(
      opened.length,
      0,
      "the independent chevron still only changes expansion",
    );
    assert.equal(expanded.length, 1);
    await click(bank.querySelector(".account-tree-amount"));
    assert.deepEqual(
      opened,
      ["account:bank"],
      "the original account-details action is preserved",
    );
    await render(
      React.createElement(AccountTree, {
        ...viewProps,
        selected: "account:bank",
      }),
    );
    const selectedAction = document.querySelector(
      '[data-account-code="bank"] .account-tree-amount',
    );
    const selectedLabel = document.querySelector(
      '[data-account-code="bank"] .account-tree-label',
    );
    Object.defineProperties(selectedLabel, {
      clientWidth: { configurable: true, value: 80 },
      scrollWidth: { configurable: true, value: 220 },
    });
    await React.act(async () => selectedAction.blur());
    await React.act(async () => selectedAction.focus());
    await settle();
    assert.equal(
      document.querySelector('[role="tooltip"]').textContent,
      "Checking •• 0042",
      "help stays live after selection and collection rerender",
    );
  });
});
