import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
const settle = () =>
  React.act(async () => new Promise((r) => setTimeout(r, 30)));

test("human account codes preserve leading zeros and do not replace stable account references", async () => {
  const { accountCodeLabel } = await loadComponent("src/lib/account-codes.ts");
  assert.equal(accountCodeLabel("Software", "0634"), "0634 · Software");
  assert.equal(accountCodeLabel("Software", undefined), "Software");
  const { transactionFacts, parseTransactionJournal } = await loadComponent(
    "src/lib/transaction-journal.ts",
  );
  const state = {
    accounts: [
      {
        code: "stable-internal",
        name: "Software",
        kind: "expense",
        gl_code: "0634",
      },
      {
        code: "different-internal",
        name: "Software",
        kind: "expense",
        gl_code: "0620",
      },
    ],
    cash_accounts: [{ code: "bank" }],
  };
  const csv =
    "book_id,currency,transaction_id,line_id,account_code,side,amount_minor,line_memo\r\nbook,USD,one,a,stable-internal,debit,120,\r\nbook,USD,one,b,bank,credit,120,\r\nbook,USD,split,c,stable-internal,debit,50,\r\nbook,USD,split,d,different-internal,debit,70,\r\nbook,USD,split,e,bank,credit,120,\r\n";
  const journal = parseTransactionJournal(csv, {
    id: "book",
    currency: "USD",
    scale: 2,
  });
  assert.equal(
    transactionFacts(state, journal)({ id: "one", kind: "journal" }).category,
    "0634 · Software",
  );
  assert.equal(
    transactionFacts(state, journal)({ id: "split", kind: "journal" }).category,
    "Split (2)",
  );
});

test("the shared account-code editor submits exact human code, stable reference and revision", async () =>
  withDOM(async ({ render, window }) => {
    const { AccountCodeEditor } = await loadComponent(
      "unit/helpers/account-codes.tsx",
    );
    const previous = globalThis.fetch;
    const writes = [];
    globalThis.fetch = async (url, options) => ({
      ok: true,
      status: 200,
      json: async () => {
        if (String(url).endsWith("/session"))
          return { csrf: "synthetic-csrf" };
        if (options?.method === "POST") {
          writes.push(JSON.parse(options.body));
          return {
            account_reference: "qb-stable",
            gl_code: "0634",
            revision: 3,
          };
        }
        return {
          account_reference: "qb-stable",
          gl_code: "",
          revision: 2,
          history: [],
        };
      },
    });
    try {
      await render(
        React.createElement(AccountCodeEditor, {
          book: "synthetic-book",
          account: { code: "qb-stable", name: "Software" },
        }),
      );
      await React.act(async () => document.querySelector("button").click());
      await settle();
      const input = document.querySelector('input[name="gl_code"]');
      assert.ok(input && !input.disabled);
      await React.act(async () => {
        Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype,
          "value",
        ).set.call(input, "0634");
        input.dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      await React.act(async () =>
        document
          .querySelector("form")
          .dispatchEvent(
            new window.Event("submit", { bubbles: true, cancelable: true }),
          ),
      );
      await settle();
      assert.equal(writes.length, 1);
      assert.equal(writes[0].account_reference, "qb-stable");
      assert.equal(writes[0].gl_code, "0634");
      assert.equal(writes[0].revision, 2);
      assert.ok(writes[0].idempotency_key);
      assert.equal(writes[0].book_id, "synthetic-book");
    } finally {
      globalThis.fetch = previous;
    }
  }));

test("Accounts tree shows assigned codes without a visibility prop or preference provider", () =>
  withDOM(async ({ render, window }) => {
    const { AccountTree } = await loadComponent(
      "unit/helpers/account-codes.tsx",
    );
    const { accountHierarchy } = await loadComponent(
      "src/lib/account-hierarchy.ts",
    );
    const accounts = [
      { code: "bank", name: "Checking", kind: "asset", gl_code: "0100" },
      { code: "reserve", name: "Reserve", kind: "asset" },
    ];
    window.localStorage.setItem(
      "vult.account-code-visibility.v1:user:synthetic-user-a",
      "false",
    );
    await render(
      React.createElement(AccountTree, {
        roots: accountHierarchy(accounts),
        book: { id: "synthetic", currency: "USD", scale: 2 },
        amounts: new Map([
          ["bank", 1200n],
          ["reserve", 200n],
        ]),
        expanded: new Set(),
        onExpanded() {},
        compare: (a, b) => a.name.localeCompare(b.name),
        nodeName: (node) => node.name,
        accountName: (account) => account.name,
        accountSuffix: () => "",
        icon: () => null,
        customize: () => null,
        grip: () => null,
        partial: () => false,
        onOpen() {},
        contextId: "code-tree",
      }),
    );
    assert.match(
      document.querySelector('[data-account-code="bank"]').textContent,
      /0100 · Checking/,
    );
    assert.doesNotMatch(
      document.querySelector('[data-account-code="reserve"]').textContent,
      /0100|reserve ·/,
    );
    assert.equal(document.querySelectorAll("input").length, 0);
  }));
