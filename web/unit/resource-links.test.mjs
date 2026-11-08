import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";
import {
  fixture,
  source,
  click,
  trigger,
  preview,
  table,
  response,
} from "./helpers/sources.mjs";

const book = {
  founder: "Fixture",
  start_date: "2026-01-01",
  profile: "business",
  accounting_timezone: "UTC",
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  address: {
    book_id: "11111111-1111-4111-8111-111111111111",
    code: "K7FM",
    slug: "synthetic",
    revision: 1,
    aliases: ["k7fm", "synthetic"],
  },
};

test("the real host keeps a legacy dashboard preview on its canonical Activity resource after startup effects", () =>
  withDOM(async ({ window, render }) => {
    const saved = globalThis.fetch;
    const row = {
      ...entry,
      book_id: book.id,
      kind: "income",
      description: "Synthetic readable entry",
      counterparty: "Fixture",
      date: "2026-10-01",
      economic_date: "2026-10-01",
      recorded_at: "2026-10-01T12:00:00Z",
      currency: "USD",
      scale: 2,
      amount_minor: "100",
      fx: {
        original_currency: "USD",
        original_amount_minor: "100",
        paid_currency: "USD",
        paid_amount_minor: "100",
        selection: "same-currency",
      },
    };
    const data = {
      book,
      currencies: [{ code: "USD", name: "US Dollar", scale: 2 }],
      transactions: [row],
      cash_accounts: [],
      payables: [],
      accounts: [],
      account_balances: [],
      months: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      summary: {
        cash_minor: "0",
        reimbursements_minor: "0",
        loans_minor: "0",
        owed_minor: "0",
        income_minor: "100",
        expenses_minor: "0",
      },
    };
    globalThis.fetch = async (url) => {
      const p = new URL(url, "http://localhost").pathname;
      if (p.endsWith("/session")) return response({ csrf: "fixture" });
      if (p.includes("/links/entry/"))
        return response({ ...entry, book_id: book.id });
      if (p.endsWith("/documents")) return response([]);
      if (p.endsWith("/sources"))
        return response({ sources: [], has_more: false });
      if (p.endsWith(entry.id))
        return response({ ...row, lines: [], allocations: [] });
      return response(data);
    };
    try {
      window.history.replaceState(
        null,
        "",
        `/dashboard?entryBook=${book.id}&entryPreview=${entry.id}`,
      );
      const app = await loadApplication();
      await render(
        React.createElement(
          app.Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(app.BookWorkspace, {
            books: [book],
            selectedBook: book,
            creationKey: "fixture",
            focusHeading: false,
            onSelect() {},
            onCreated() {},
            onBooksChanged() {},
            initialState: data,
          }),
        ),
      );
      assert.equal(window.location.pathname, "/synthetic/activity/7M4X9Q2Z");
      assert.equal(window.location.search, "");
      assert.match(
        document.querySelector(".entry-preview-content").textContent,
        /Synthetic readable entry/,
      );
      assert.doesNotMatch(
        document.body.textContent,
        /couldn’t refresh your activity/,
      );
    } finally {
      globalThis.fetch = saved;
    }
  }));
const entry = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  reference: "7M4X9Q2Z",
};
const next = {
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  reference: "N8K2WP3R",
};

test("readable resource paths retain dates, filters and Book scope; mixed/foreign links do not guess", () =>
  withDOM(async ({ window }) => {
    const p = await loadComponent("src/lib/resource-links.ts");
    const url = p.resourceURL(
      book,
      "entry",
      entry.reference,
      `http://localhost/synthetic/activity?entryPreview=${entry.id}&entryBook=${book.id}&from=2026-01-01&to=2026-12-31&kind=income`,
    );
    assert.equal(url.pathname, "/synthetic/activity/7M4X9Q2Z");
    assert.equal(url.search, "?from=2026-01-01&to=2026-12-31&kind=income");
    assert.equal(p.resourceSelection(book, "entry", url.href), entry.reference);
    assert.equal(p.resourceSelection(book, "source", url.href), "");
    assert.equal(
      p.resourceSelection(
        book,
        "entry",
        `http://localhost/activity?entryPreview=${entry.id}&entryBook=other`,
      ),
      "",
    );
    assert.throws(() =>
      p.resourceSelection(book, "entry", url.href + "&entryPreview=" + next.id),
    );
    assert.equal(
      p.clearResourceSelection(url, "entry").pathname,
      "/synthetic/activity",
    );
    assert.equal(
      p.resourceURL(book, "source", next.reference).pathname,
      "/synthetic/sources/N8K2WP3R",
    );
  }));

test("new entry links resolve to UUIDs once; history and pending drafts retain inspector identity", () =>
  withDOM(async ({ window, render }) => {
    const { useResourcePreview } = await loadComponent(
      "src/lib/use-resource-preview.ts",
    );
    const saved = globalThis.fetch,
      reads = [];
    globalThis.fetch = async (url) => {
      reads.push(url);
      return response({ ...entry, book_id: book.id });
    };
    let value,
      pending = false;
    function Harness() {
      value = useResourcePreview(book, "entry", pending, [entry, next]);
      return React.createElement("textarea", {
        defaultValue: "Retained reason",
      });
    }
    try {
      window.history.replaceState(
        { retained: "history" },
        "",
        "/synthetic/activity/7M4X9Q2Z?kind=income",
      );
      await render(React.createElement(Harness));
      assert.equal(value.detail, entry.id);
      const draft = document.querySelector("textarea");
      draft.value = "My unsaved correction";
      await React.act(async () => value.show(next.id));
      assert.equal(window.location.pathname, "/synthetic/activity/N8K2WP3R");
      assert.equal(window.location.search, "?kind=income");
      assert.deepEqual(window.history.state, { retained: "history" });
      assert.equal(document.querySelector("textarea"), draft);
      pending = true;
      await render(React.createElement(Harness));
      await React.act(async () => {
        window.history.replaceState(null, "", "/synthetic/activity/7M4X9Q2Z");
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(value.detail, next.id);
      assert.equal(window.location.pathname, "/synthetic/activity/N8K2WP3R");
      assert.equal(draft.value, "My unsaved correction");
      pending = false;
      await render(React.createElement(Harness));
      await React.act(async () => {
        window.history.replaceState(
          { legacy: true },
          "",
          `/synthetic/activity?entryBook=${book.id}&entryPreview=${entry.id}`,
        );
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(value.detail, entry.id);
      assert.equal(window.location.pathname, "/synthetic/activity/7M4X9Q2Z");
      assert.equal(window.location.search, "");
      assert.equal(
        reads.length,
        0,
        "a verified loaded row already supplies its reference",
      );
    } finally {
      globalThis.fetch = saved;
    }
  }));

