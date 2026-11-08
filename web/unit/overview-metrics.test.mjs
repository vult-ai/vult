import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
const book = {
  id: "book-a",
  start_date: "2026-01-01",
  currency: "USD",
  scale: 2,
};
const state = {
  accounts: [
    ["cash", "asset"],
    ["card", "liability"],
    ["capital", "equity"],
    ["cost", "expense"],
  ].map(([code, kind]) => ({ code, kind, name: code })),
};
function entry(debit, credit, amount, date, id) {
  return [
    [debit, "debit"],
    [credit, "credit"],
  ].map(([account_code, side], i) => ({
    book_id: book.id,
    currency: "USD",
    scale: "2",
    economic_date: date,
    line_id: `${id}-${i}`,
    account_code,
    side,
    amount_minor: amount,
  }));
}
const journal = [
  ...entry("cash", "capital", "10000", "2026-01-01", "fund"),
  ...entry("cost", "card", "4000", "2026-01-02", "spend"),
  ...entry("card", "cash", "1000", "2026-01-03", "pay"),
  ...entry("cash", "capital", "999999", "2026-01-04", "future"),
];
test("overview history separates credit-card spending from cash, retains economic dates and leaves journals untouched", async () => {
  const { overviewHistory } = await loadComponent(
    "src/lib/overview-history.ts",
  );
  const before = JSON.stringify(journal);
  assert.deepEqual(overviewHistory(book, state, journal, "2026-01-03"), [
    { date: "2026-01-01", assets: "10000", liabilities: "0", equity: "10000" },
    {
      date: "2026-01-02",
      assets: "10000",
      liabilities: "4000",
      equity: "6000",
    },
    { date: "2026-01-03", assets: "9000", liabilities: "3000", equity: "6000" },
  ]);
  assert.equal(JSON.stringify(journal), before);
  assert.throws(
    () =>
      overviewHistory(
        { ...book, id: "another-book" },
        state,
        journal,
        "2026-01-03",
      ),
    /different Book/,
  );
});
test("overview snapshots preserve reversals, exact large amounts and empty date ranges", async () => {
  const { overviewHistory } = await loadComponent(
    "src/lib/overview-history.ts",
  );
  const amount = "90071992547409930";
  const rows = [
    ...entry("cash", "capital", amount, "2026-01-01", "big"),
    ...entry("capital", "cash", amount, "2026-01-03", "reverse"),
  ];
  const history = overviewHistory(book, state, rows, "2026-01-03");
  assert.equal(history[0].assets, amount);
  assert.equal(history[1].equity, amount);
  assert.equal(history[2].assets, "0");
  assert.deepEqual(overviewHistory(book, state, rows, "2025-12-31"), []);
});
test("comparison windows use exact recorded snapshots, prior equal-length dates and no pre-Book history", async () => {
  const { overviewComparison } = await loadComponent(
    "src/lib/overview-history.ts",
  );
  const original = JSON.stringify(journal);
  const result = overviewComparison(book, state, journal, {
    from: "2026-01-03",
    to: "2026-01-04",
  });
  assert.deepEqual(result.previousRange, {
    from: "2026-01-01",
    to: "2026-01-02",
  });
  assert.deepEqual(
    result.current.map((p) => [p.date, p.assets]),
    [
      ["2026-01-03", "9000"],
      ["2026-01-04", "1008999"],
    ],
  );
  assert.deepEqual(
    result.previous.map((p) => [p.date, p.assets]),
    [
      ["2026-01-01", "10000"],
      ["2026-01-02", "10000"],
    ],
  );
  assert.deepEqual(
    overviewComparison(book, state, journal, {
      from: "2026-01-01",
      to: "2026-01-02",
    }).previous,
    [],
  );
  assert.equal(JSON.stringify(journal), original);
});
test("current and previous balance lines share one value scale", async () => {
  await withDOM(async ({ render }) => {
    const { BalanceTrend } = await loadComponent(
      "src/components/v0/balance-trend.tsx",
    );
    await render(
      React.createElement(BalanceTrend, {
        name: "Assets",
        book,
        current: [
          { date: "2026-01-01", amount: "100" },
          { date: "2026-01-02", amount: "200" },
        ],
        previous: [
          { date: "2025-12-30", amount: "100" },
          { date: "2025-12-31", amount: "1000" },
        ],
      }),
    );
    const current = document
      .querySelector("[data-trend-current]")
      .getAttribute("d");
    const previous = document
      .querySelector("[data-trend-previous]")
      .getAttribute("d");
    assert.equal(
      current.split(" ")[0],
      previous.split(" ")[0],
      "equal balances align on both lines",
    );
    const lastY = (path) => Number(path.split(",").at(-1));
    assert.ok(
      lastY(current) > lastY(previous),
      "the smaller balance stays below the larger one",
    );
    assert.match(
      document.querySelector("svg").textContent,
      /Both use the same scale/,
    );
    const chart = document.querySelector('[role="img"]');
    assert.equal(
      chart.querySelector("title"),
      null,
      "native browser hover popups are disabled",
    );
    assert.equal(
      chart.querySelector("desc").id,
      chart.getAttribute("aria-describedby"),
    );
    assert.match(chart.getAttribute("aria-label"), /ending \$2\.00/);
  });
});

test("unavailable balance history uses normal text outside stretched chart coordinates", async () => {
  await withDOM(async ({ render }) => {
    const { BalanceTrend } = await loadComponent(
      "src/components/v0/balance-trend.tsx",
    );
    await render(React.createElement(BalanceTrend, { name: "Assets", book }));
    const message = document.querySelector(".balance-trend-empty");
    assert.equal(message.textContent, "History unavailable");
    assert.equal(message.namespaceURI, "http://www.w3.org/1999/xhtml");
    assert.equal(message.closest("svg"), null);
    assert.ok(message.classList.contains("vult-type-metadata"));
    assert.equal(document.querySelector("svg text"), null);
    assert.match(
      document.querySelector('[role="img"]').getAttribute("aria-label"),
      /Assets: recorded trend unavailable/,
    );
    assert.equal(document.querySelector("[data-trend-current]"), null);
  });
});
