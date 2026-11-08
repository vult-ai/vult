import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "book", name: "Synthetic", currency: "USD", scale: 2 };
const currencies = [
  { code: "USD", scale: 2 },
  { code: "EUR", scale: 2 },
];
const transaction = (id, description) => ({
  id,
  attachment_count: 0,
  description,
  date: "2026-10-12",
  kind: "expense",
  payer: "business",
  category: "5000",
  amount_minor: "11000",
  counterparty: "Example",
  fx: { original_currency: "EUR", original_amount_minor: "10000" },
});

test("recorded Activity rows without an original valuation show the Book amount", async () => {
  await withDOM(async ({ render }) => {
    const { default: Transactions } = await loadComponent("src/components/book/transactions-table.tsx");
    await render(React.createElement(Transactions, {
      state: {
        currencies, accounts: [],
        transactions: [{
          id: "recorded", kind: "journal", description: "Recorded entry",
          date: "2026-10-12", amount_minor: "1250",
        }],
      },
      book, detail: "", loading: false, resetKey: "recorded",
      emptyState: "Empty", onOpen() {},
    }));
    assert.equal(document.querySelector(".transaction-ledger")?.textContent, "$12.50");
    assert.equal(document.querySelector(".transaction-native"), null);
  });
});

test("dates omit only the current calendar year across filtering, ordering and New Year", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: new Date(2026, 9, 27) });
  await withDOM(async ({ render }) => {
    const { default: Transactions } = await loadComponent(
      "src/components/book/transactions-table.tsx",
    );
    const dates = [
      "2026-10-20",
      "2026-09-01",
      "2025-12-31",
      "2025-09-01",
      "2027-01-01",
    ];
    const state = {
      currencies,
      accounts: [{ code: "5000", name: "Software" }],
      transactions: dates.map((date, index) => ({
        ...transaction(String(index), `Entry ${index}`),
        date,
      })),
    };
    const view = (transactions, resetKey) =>
      React.createElement(Transactions, {
        state: { ...state, transactions },
        book,
        detail: "",
        loading: false,
        resetKey,
        emptyState: "Empty",
        onOpen: () => {},
      });
    await render(view(state.transactions, "all"));
    const cells = [...document.querySelectorAll("time.transaction-date")];
    const visibleDates = () =>
      [...document.querySelectorAll("time.transaction-date > [aria-hidden]")]
        .map((cell) => cell.textContent);
    assert.deepEqual(visibleDates(), [
      "Oct 20", "Sep 1", "Dec 31, 2025", "Sep 1, 2025", "Jan 1, 2027",
    ]);
    assert.deepEqual(
      cells.map((cell) => cell.dateTime),
      dates,
    );
    assert.equal(
      cells[1].querySelector(".sr-only").textContent,
      "September 1, 2026",
    );
    assert.equal(
      cells[2].title,
      "2025-12-31",
    );
    await render(view(state.transactions.slice(2, 4), "filtered"));
    assert.deepEqual(visibleDates(), ["Dec 31, 2025", "Sep 1, 2025"]);
    await render(view([...state.transactions].reverse(), "reordered"));
    assert.deepEqual(visibleDates(), [
      "Jan 1, 2027", "Sep 1, 2025", "Dec 31, 2025", "Sep 1", "Oct 20",
    ]);
    t.mock.timers.setTime(new Date(2027, 0, 1).getTime());
    await render(view(state.transactions, "new-year"));
    assert.deepEqual(visibleDates(), [
      "Oct 20, 2026", "Sep 1, 2026", "Dec 31, 2025", "Sep 1, 2025", "Jan 1",
    ]);
  });
});

test("phone rows retain Date and To/From with the primary amount and full accessible values", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: new Date(2026, 9, 27) });
  await withDOM(async ({ render, window }) => {
    window.matchMedia = (query) => ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    });
    const { default: Transactions } = await loadComponent(
      "src/components/book/transactions-table.tsx",
    );
    const opened = [];
    await render(
      React.createElement(Transactions, {
        state: {
          currencies,
          accounts: [{ code: "5000", name: "Software" }],
          transactions: [transaction("a", "Entry A")],
        },
        book,
        detail: "",
        loading: false,
        resetKey: "phone",
        emptyState: "Empty",
        onOpen: (id) => opened.push(id),
      }),
    );
    const table = document.querySelector("table");
    assert.deepEqual(
      [...table.querySelectorAll("th")].map((cell) => cell.textContent),
      ["Transactions"],
    );
    assert.equal(table.querySelector("time .sr-only").textContent, "October 12, 2026");
    assert.equal(table.querySelector("time [aria-hidden]").textContent, "Oct 12");
    assert.equal(table.querySelector("time").title, "2026-10-12");
    const amount = table.querySelector(".transaction-money");
    assert.match(amount.textContent, /\$110.00/);
    assert.equal(amount.querySelector(".transaction-ledger").textContent, "$110.00");
    assert.equal(amount.querySelector(".transaction-native").textContent, "€100.00");
    assert.match(amount.getAttribute("aria-label"), /€100.00 original/);
    await React.act(async () => table.querySelector(".entry-link").click());
    assert.deepEqual(
      opened,
      ["a"],
      "the identity opens details without a separate utility column",
    );
    assert.equal(
      table.querySelector('input[aria-label="Select Entry A"]').checked,
      false,
    );
  });
});

test("the real transaction consumer separates selection from inspection and retains its frame and filters", async () => {
  await withDOM(async ({ render, window }) => {
    const { default: Transactions } = await loadComponent(
      "src/components/book/transactions-table.tsx",
    );
    const state = {
      currencies,
      accounts: [{ code: "5000", name: "Software" }],
      transactions: [
        transaction("a", "Entry A"),
        {
          ...transaction("b", "Entry B"),
          kind: "journal",
          category: "",
          import: { review: { category: "Subscriptions" } },
        },
      ],
    };
    const opened = [];
    const view = (loading) =>
      React.createElement(Transactions, {
        state,
        book,
        detail: "",
        loading,
        resetKey: "same-view",
        emptyState: "Empty",
        toolbar: React.createElement("input", {
          "aria-label": "Retained filter",
          value: "EUR expenses",
          readOnly: true,
        }),
        onOpen: (id) => opened.push(id),
      });
    await render(view(false));
    const table = document.querySelector("table");
    assert.equal(table.getAttribute("aria-multiselectable"), "true");
    assert.equal(table.querySelectorAll('input[type="checkbox"]').length, 3);
    assert.deepEqual(
      [...table.querySelectorAll("th")].map((node) => node.textContent),
      ["Date", "To/From", "Amount", "Category"],
    );
    const firstCells = table.querySelector('tr[data-record-id="a"]').cells;
    assert.equal(
      firstCells[1].querySelector(".transaction-counterparty-name").textContent,
      "Example",
    );
    assert.equal(
      firstCells[1]
        .querySelector("[data-counterparty-avatar]")
        .getAttribute("aria-hidden"),
      "true",
    );
    assert.equal(firstCells[1].querySelector("small"), null);
    assert.equal(firstCells[3].textContent, "Software");
    assert.equal(
      table.querySelector('tr[data-record-id="b"]').cells[3].textContent,
      "Subscriptions",
    );
    const first = table.querySelector('input[aria-label="Select Entry A"]');
    assert.ok(first, "each record needs its canonical selector");
    await React.act(async () => first.click());
    assert.equal(first.checked, true);
    assert.deepEqual(
      opened,
      [],
      "checkbox selection must not open financial details",
    );
    assert.match(
      document.querySelector('[role="status"]').textContent,
      /1 selected/,
    );
    await render(view(true));
    assert.equal(
      document.querySelector("table"),
      table,
      "a read cannot remount the grid",
    );
    assert.equal(
      table.querySelector('input[aria-label="Select Entry A"]').checked,
      true,
    );
    const clear = document.querySelector(
      'button[aria-label="Clear selection"]',
    );
    await React.act(async () => clear.focus());
    const headerSelector = table.querySelector(
      'input[aria-label="Select rows on this page"]',
    );
    const focusRequests = [];
    const focus = headerSelector.focus.bind(headerSelector);
    headerSelector.focus = (options) => {
      focusRequests.push(options);
      focus(options);
    };
    await React.act(async () => clear.click());
    assert.deepEqual(focusRequests, [{ preventScroll: true }]);
    assert.equal(
      document.querySelector('[aria-label="Retained filter"]').value,
      "EUR expenses",
    );
    const values = [...table.querySelectorAll("td")].map((e) => e.textContent);
    assert.ok(
      values.some((value) => /\$110\.00.*€100\.00/.test(value)),
      "the exact book-currency value precedes its trailing original FX value",
    );
    await React.act(async () =>
      table.querySelector('button[aria-label="View Entry A details"]').click(),
    );
    assert.deepEqual(opened, [], "a pending read must not open stale details");
    await render(view(false));
    assert.equal(document.querySelector("table"), table);
    assert.equal(table.querySelector("time").dateTime, "2026-10-12");
    assert.equal(table.querySelector("time .sr-only").textContent, "October 12, 2026");
    await React.act(async () =>
      table.querySelector('button[aria-label="View Entry A details"]').click(),
    );
    assert.deepEqual(opened, ["a"]);
    const row = table.querySelector('tr[data-record-id="a"]');
    const amountCell = row.cells[2];
    const rowFocus = row.focus;
    let extraFocus = 0;
    row.focus = function (options) {
      extraFocus++;
      rowFocus.call(this, options);
    };
    try {
      await React.act(async () =>
        amountCell.dispatchEvent(
          new window.MouseEvent("pointerdown", { bubbles: true, button: 0 }),
        ),
      );
      assert.equal(
        extraFocus,
        0,
        "a pointer press must not focus the row before native cell focus",
      );
    } finally {
      row.focus = rowFocus;
    }
    await React.act(async () => {
      amountCell.focus();
      amountCell.click();
    });
    assert.deepEqual(opened, ["a", "a"]);
    assert.equal(
      first.checked,
      false,
      "pointer inspection must not change checkbox selection",
    );
    await React.act(async () => amountCell.focus());
    await React.act(async () => {
      amountCell.dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
      amountCell.dispatchEvent(
        new window.KeyboardEvent("keyup", { key: "Enter", bubbles: true }),
      );
    });
    assert.deepEqual(
      opened,
      ["a", "a", "a"],
      "Enter on a passive cell inspects the same record",
    );
    assert.equal(
      first.checked,
      false,
      "keyboard inspection must not toggle checkbox selection",
    );
    state.transactions = [
      {
        ...transaction("c", "An entry with several journal accounts"),
        kind: "journal",
        counterparty: "",
        category: "",
      },
    ];
    await render(view(false));
    const journalCells = table.querySelector('tr[data-record-id="c"]').cells;
    assert.equal(
      journalCells[1].querySelector(".transaction-counterparty-name").textContent,
      "An entry with several journal accounts",
      "a missing party keeps the exact source description on one line",
    );
    assert.equal(
      journalCells[3].textContent,
      "—",
      "an unprojected journal must not be guessed as a category, split or uncategorized",
    );
  });
});

test("counterparty avatars share approved originals, keep unlinked parties on initials and open the same details", async () => {
  await withDOM(async ({ render, window }) => {
    const meta = document.createElement("meta");
    meta.name = "vult-shared-asset-origin";
    meta.content = "https://assets.example";
    document.head.append(meta);
    const catalog = readFileSync(
      new URL("./fixtures/shared-assets/catalog.v1.json", import.meta.url),
      "utf8",
    );
    const requests = [];
    const previousFetch = globalThis.fetch;
    globalThis.fetch = async (url, options) => {
      requests.push({ url, options });
      if (String(url).startsWith("/api/v1/entry-brands?"))
        return { ok: false, status: 404, json: async () => ({}) };
      return {
        ok: true,
        headers: {
          get: (key) =>
            key === "content-type"
              ? "application/json"
              : String(catalog.length),
        },
        text: async () => catalog,
      };
    };
    try {
      const { default: Transactions } = await loadComponent(
        "src/components/book/transactions-table.tsx",
      );
      const rows = [
        { ...transaction("a", "First payment"), counterparty: "Wise" },
        { ...transaction("b", "Second payment"), counterparty: " wise " },
        { ...transaction("c", "Wise"), counterparty: "" },
        {
          ...transaction("d", "Merchant payment"),
          counterparty: "Google Cloud",
        },
      ];
      const opened = [];
      await render(
        React.createElement(Transactions, {
          state: { currencies, accounts: [], transactions: rows },
          book,
          detail: "",
          loading: false,
          resetKey: "identities",
          emptyState: "Empty",
          onOpen: (id) => opened.push(id),
        }),
      );
      const row = (id) => document.querySelector(`tr[data-record-id="${id}"]`);
      const avatar = (id) =>
        row(id).querySelector("[data-counterparty-avatar]");
      assert.equal(
        requests.filter(({ url }) => String(url).startsWith("https://assets.example/")).length,
        1,
        "one catalog read serves repeated counterparties",
      );
      const publicRead = requests.find(({ url }) => String(url).startsWith("https://assets.example/"));
      assert.equal(publicRead.url, "https://assets.example/brand-logos.v1.json");
      assert.equal(publicRead.options.credentials, "omit");
      assert.equal(publicRead.options.referrerPolicy, "no-referrer");
      assert.equal(
        avatar("a").querySelector("img").src,
        avatar("b").querySelector("img").src,
      );
      assert.equal(avatar("a").dataset.tone, avatar("b").dataset.tone);
      assert.equal(
        avatar("c").querySelector("img"),
        null,
        "a description alone cannot link a brand",
      );
      assert.ok(avatar("c").textContent.trim(), "the unlinked record has an initials fallback");
      assert.equal(avatar("d").querySelector("img"), null);
      assert.equal(avatar("d").textContent, "GC");
      assert.equal(avatar("a").querySelector("button,a,[tabindex]"), null);
      await React.act(async () => avatar("a").click());
      assert.deepEqual(
        opened,
        ["a"],
        "the avatar shares the existing identity action",
      );
      assert.equal(
        row("a").querySelector('input[type="checkbox"]').checked,
        false,
      );
      await React.act(async () =>
        avatar("a")
          .querySelector("img")
          .dispatchEvent(new window.Event("error")),
      );
      assert.equal(avatar("a").querySelector("img"), null);
      assert.equal(avatar("a").textContent, "WI");
    } finally {
      globalThis.fetch = previousFetch;
    }
  });
});

test("the identity mouse press preserves an existing checkbox selection", async () => {
  await withDOM(async ({ render, window }) => {
    const { default: Transactions } = await loadComponent("src/components/book/transactions-table.tsx");
    const opened = [];
    await render(React.createElement(Transactions, {
      state: {currencies, accounts: [{code: "5000", name: "Software"}], transactions: [transaction("a", "Entry A")]},
      book, detail: "", loading: false, resetKey: "press", emptyState: "Empty", onOpen: id => opened.push(id),
    }));
    const selector = document.querySelector('input[aria-label="Select Entry A"]');
    await React.act(async () => selector.click());
    assert.equal(selector.checked, true);
    const button = document.querySelector('button[aria-label="View Entry A details"]');
    await React.act(async () => {
      button.dispatchEvent(new window.MouseEvent("mousedown", {bubbles: true, button: 0, buttons: 1}));
      button.dispatchEvent(new window.MouseEvent("mouseup", {bubbles: true, button: 0}));
      button.click();
    });
    assert.deepEqual(opened, ["a"]);
    assert.equal(selector.checked, true, "the identity control must own its press rather than toggling the row selector");

  });
});
