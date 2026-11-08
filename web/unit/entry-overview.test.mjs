import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const line = (id, code, name, side, amount = "66") => ({
  id,
  account_code: code,
  account_name: name,
  side,
  amount_minor: amount,
});
const state = {
  accounts: [
    { code: "bank-a", name: "Wise USD", kind: "asset" },
    { code: "bank-b", name: "Operating", kind: "asset" },
    {
      code: "interest",
      name: "Interest earned",
      kind: "other_income",
    },
    { code: "software", name: "Software", kind: "expense" },
  ],
  cash_accounts: [
    { code: "bank-a", name: "Wise USD" },
    { code: "bank-b", name: "Operating" },
  ],
  account_balances: [],
};
const income = {
  id: "synthetic",
  kind: "income",
  date: "2026-10-01",
  counterparty: "Wise",
  description: "Interest payment",
  amount_minor: "66",
  note: "Synthetic provenance mentions another account; never classify from this note.",
  fx: {
    original_currency: "USD",
    original_amount_minor: "66",
    cash_account: "bank-a",
  },
  lines: [
    line("a", "bank-a", "Wise USD", "debit"),
    line("b", "interest", "Interest earned", "credit"),
  ],
};

test("entry context comes from its exact postings and explicit account metadata, without mutating the record", async () => {
  const { entryOverview, entryDisplayDate } = await loadComponent(
    "src/lib/entry-overview.ts",
  );
  const before = JSON.stringify({ state, income });
  const actual = entryOverview(income, state);
  assert.equal(actual.label, "Received");
  assert.equal(actual.category, "Interest earned");
  assert.deepEqual(actual.accounts, [
    { code: "bank-a", name: "Wise USD", label: "Account" },
  ]);
  assert.equal(entryDisplayDate(income.date), "October 1, 2026");
  assert.equal(JSON.stringify({ state, income }), before);
  const lean = entryOverview(income, {
    ...state,
    accounts: [],
    cash_accounts: [],
  });
  assert.equal(
    lean.category,
    "Interest earned",
    "explicit cash reference excludes the bank from category projection even with a lean chart",
  );
  assert.equal(
    lean.accounts[0].name,
    "Wise USD",
    "the selected posting supplies its account name",
  );
});

test("transfers expose both accounts, while an unclassified asset is never guessed to be cash", async () => {
  const { entryOverview } = await loadComponent("src/lib/entry-overview.ts");
  const transfer = entryOverview(
    {
      ...income,
      kind: "transfer",
      fx: {},
      lines: [
        line("a", "bank-a", "Wise USD", "debit"),
        line("b", "bank-b", "Operating", "credit"),
      ],
    },
    state,
  );
  assert.equal(transfer.label, "Transfer");
  assert.equal(transfer.category, "");
  assert.deepEqual(
    transfer.accounts.map(({ label, name }) => [label, name]),
    [
      ["From", "Operating"],
      ["To", "Wise USD"],
    ],
  );
  const expense = entryOverview(
    {
      ...income,
      kind: "expense",
      fx: {},
      lines: [
        line("a", "software", "Software", "debit"),
        line("b", "unclassified", "Wise lookalike", "credit"),
      ],
    },
    state,
  );
  assert.equal(expense.label, "Expense");
  assert.equal(expense.category, "Software");
  assert.deepEqual(expense.accounts, []);
});

test("card purchases and reversals keep their meaning without claiming bank cash moved", async () => {
  const { entryOverview } = await loadComponent("src/lib/entry-overview.ts");
  const card = entryOverview(
    {
      ...income,
      kind: "card_purchase",
      fx: { card_account: "card" },
      lines: [
        line("a", "software", "Software", "debit"),
        line("b", "card", "Travel card", "credit"),
      ],
    },
    state,
  );
  assert.equal(card.label, "Card purchase");
  assert.deepEqual(card.accounts, [
    { code: "card", name: "Travel card", label: "Card" },
  ]);
  const reversal = entryOverview(
    {
      ...income,
      kind: "reversal",
      lines: [
        line("a", "bank-a", "Wise USD", "credit"),
        line("b", "interest", "Interest earned", "debit"),
      ],
    },
    state,
  );
  assert.equal(reversal.label, "Reversal");
  assert.equal(reversal.direction, "reversal");
});

test("foreign summaries retain three distinct currencies and valuation caveats without fabricating status from notes", async () => {
  await withDOM(async ({ render }) => {
    const { EntryOverview } = await loadComponent(
      "src/components/book/entry-overview.tsx",
    );
    const value = {
      ...income,
      kind: "expense",
      counterparty: "Synthetic supplier",
      amount_minor: "11000",
      note: "Synthetic note mentions a cleared statement; this is not structured evidence.",
      fx: {
        original_currency: "EUR",
        original_amount_minor: "10000",
        paid_currency: "GBP",
        paid_amount_minor: "9000",
        selection: "reference",
      },
      lines: [
        line("a", "software", "Software", "debit", "11000"),
        line("b", "bank-b", "Operating", "credit", "11000"),
      ],
    };
    const view = (data) =>
      React.createElement(EntryOverview, {
        data,
        state,
        book: { id: "synthetic", currency: "USD", scale: 2 },
        currencies: [
          { code: "USD", scale: 2 },
          { code: "EUR", scale: 2 },
          { code: "GBP", scale: 2 },
        ],
      });
    await render(view(value));
    const summary = document.querySelector("section");
    for (const amount of ["$110.00", "€100.00", "£90.00"])
      assert.ok(summary.textContent.includes(amount));
    assert.match(summary.textContent, /Estimated book value/);
    assert.doesNotMatch(
      summary.textContent,
      /cleared statement|Verified|Matched|Reversed|Correction/,
    );
    assert.equal(summary.querySelector("a"), null);
    await render(
      view({
        ...value,
        fx: { ...value.fx, selection: "actual" },
        reversed_by: "synthetic-reversal",
      }),
    );
    assert.doesNotMatch(summary.textContent, /Estimated book value/);
    assert.match(summary.textContent, /Reversed/);
  });
});

test("money paths distinguish outgoing, incoming, transfers, cards and neutral reversals from identified postings", async () => {
  const { entryOverview } = await loadComponent("src/lib/entry-overview.ts");
  const path = (data) =>
    entryOverview(data, state).path.map(({ kind, label, name }) => [
      kind,
      label,
      name,
    ]);
  assert.deepEqual(path(income), [
    ["counterparty", "From", "Wise"],
    ["account", "To", "Wise USD"],
  ]);
  const outgoing = {
    ...income,
    kind: "expense",
    counterparty: "Synthetic supplier",
    lines: [
      line("e", "software", "Software", "debit"),
      line("a", "bank-b", "Operating", "credit"),
    ],
  };
  assert.deepEqual(path(outgoing), [
    ["account", "From", "Operating"],
    ["counterparty", "To", "Synthetic supplier"],
  ]);
  assert.deepEqual(path({ ...outgoing, kind: "reversal" }), [
    ["account", "Account", "Operating"],
    ["counterparty", "Counterparty", "Synthetic supplier"],
  ]);
  assert.deepEqual(
    path({
      ...income,
      kind: "transfer",
      lines: [
        line("a", "bank-a", "Wise USD", "debit"),
        line("b", "bank-b", "Operating", "credit"),
      ],
    }),
    [
      ["account", "From", "Operating"],
      ["account", "To", "Wise USD"],
    ],
  );
  assert.deepEqual(
    path({
      ...outgoing,
      kind: "card_purchase",
      fx: { card_account: "card" },
      lines: [
        line("e", "software", "Software", "debit"),
        line("c", "card", "Travel card", "credit"),
      ],
    }),
    [
      ["account", "Charged to", "Travel card"],
      ["counterparty", "To", "Synthetic supplier"],
    ],
  );
  assert.deepEqual(
    path({
      ...outgoing,
      kind: "card_refund",
      fx: { card_account: "card" },
      lines: [
        line("c", "card", "Travel card", "debit"),
        line("e", "software", "Software", "credit"),
      ],
    }),
    [
      ["counterparty", "From", "Synthetic supplier"],
      ["account", "Refunded to", "Travel card"],
    ],
  );
});

test("missing cash authority, missing names and zero funding nets cannot invent a money path", async () => {
  const { entryOverview } = await loadComponent("src/lib/entry-overview.ts");
  const unclassified = {
    ...income,
    kind: "expense",
    fx: {},
    lines: [
      line("e", "software", "Software", "debit"),
      line("x", "unknown", "Wise Checking lookalike", "credit"),
    ],
  };
  assert.deepEqual(
    entryOverview(unclassified, state).path.map(({ label }) => label),
    ["Counterparty"],
  );
  assert.deepEqual(
    entryOverview({ ...unclassified, counterparty: "" }, state).path,
    [],
  );
  const offset = {
    ...income,
    lines: [
      line("a", "bank-a", "Wise USD", "debit"),
      line("b", "bank-a", "Wise USD", "credit"),
    ],
  };
  assert.deepEqual(
    entryOverview(offset, state).path.map(({ label }) => label),
    ["Counterparty"],
  );
  const incoming = entryOverview({ ...income, counterparty: "" }, state);
  assert.deepEqual(
    incoming.path.map(({ label, name }) => [label, name]),
    [["To", "Wise USD"]],
  );
});

test("account masks remain one exact suffix and ordinary numeric names remain intact", async () => {
  const { entryAccountIdentity } = await loadComponent(
    "src/lib/entry-overview.ts",
  );
  assert.deepEqual(entryAccountIdentity("Example Checking •• 4821 - 2"), {
    name: "Example Checking",
    suffix: "•• 4821 - 2",
  });
  assert.deepEqual(entryAccountIdentity("Capital One 360"), {
    name: "Capital One 360",
    suffix: "",
  });
});

test("the summary leads with who the entry is about, its amount, kind and full date, and keeps the original purpose", async () => {
  await withDOM(async ({ render }) => {
    const { EntryOverview } = await loadComponent(
      "src/components/book/entry-overview.tsx",
    );
    const data = {
      ...income,
      kind: "expense",
      counterparty: "Synthetic supplier",
      description: "Original purpose — September services",
      fx: { ...income.fx, cash_account: "bank-b" },
      lines: [
        line("a", "bank-b", "Operating", "credit"),
        line("e", "software", "Software", "debit"),
      ],
    };
    const before = JSON.stringify({ data, state });
    await render(
      React.createElement(EntryOverview, {
        data,
        state,
        book: { id: "synthetic", currency: "USD", scale: 2 },
        currencies: [{ code: "USD", scale: 2 }],
      }),
    );
    const summary = document.querySelector('[aria-label="Transaction summary"]');
    const hero = summary.querySelector(".entry-hero");
    assert.equal(summary.firstElementChild, hero);
    assert.equal(hero.querySelector("h3").textContent, data.counterparty);
    assert.equal(hero.querySelector(".entry-hero-title p").textContent, data.description);
    assert.ok(hero.querySelector("[data-counterparty-avatar], [data-account-avatar]"), "the party has an avatar");
    assert.equal(hero.querySelector(".entry-hero-amount .rolling-number-text").textContent, "$0.66");
    assert.match(hero.querySelector(".entry-chip").textContent, /^Paid$/);
    assert.equal(hero.querySelector(".entry-chip").dataset.tone, "out");
    assert.equal(hero.querySelector("time").getAttribute("datetime"), data.date);
    assert.doesNotMatch(summary.textContent, /Entry ID|Synthetic provenance/);
    assert.match(summary.textContent, /CategorySoftware/);
    assert.equal(JSON.stringify({ data, state }), before);
  });
});
