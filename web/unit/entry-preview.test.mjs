import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";
import { inspectorSize } from "./helpers/inspector-size.mjs";
import { decoded } from "./helpers/document-engine.mjs";

const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic book",
  founder: "Example",
  currency: "USD",
  scale: 2,
  profile: "business",
  accounting_timezone: "UTC",
  start_date: "2026-10-01",
};
const other = {
  ...book,
  id: "22222222-2222-4222-8222-222222222222",
  name: "Other synthetic book",
};
const currencies = [{ code: "USD", name: "US Dollar", scale: 2 }];
const ids = [
  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
];
const entries = ids.map((id, i) => ({
  id,
  description: `Entry ${i ? "B" : "A"}`,
  date: "2026-10-12",
  kind: "expense",
  payer: "business",
  category: "5000",
  amount_minor: "11000",
  recorded_at: "2026-10-12T12:00:00Z",
  fx: {
    original_currency: "USD",
    original_amount_minor: "11000",
    paid_currency: "USD",
    paid_amount_minor: "11000",
    selection: "same-currency",
  },
}));
const state = {
  book,
  currencies,
  summary: {
    cash_minor: "0",
    reimbursements_minor: "0",
    loans_minor: "0",
    owed_minor: "0",
    income_minor: "0",
    expenses_minor: "0",
  },
  cash_accounts: [],
  payables: [],
  fx_gain_minor: "0",
  fx_loss_minor: "0",
  accounts: [{ code: "5000", name: "Software", kind: "expense" }],
  transactions: entries,
};
const detail = (id) => ({
  ...entries.find((x) => x.id === id),
  allocations: [],
  lines: [
    {
      id: "line-1",
      account_code: "5000",
      account_name: "Software",
      side: "debit",
      amount_minor: "11000",
    },
    {
      id: "line-2",
      account_code: "1000",
      account_name: "Business cash",
      side: "credit",
      amount_minor: "11000",
    },
  ],
});
const response = (data, ok = true) => ({ ok, json: async () => data });
const action = (id) =>
  document.querySelector(
    `button[aria-label="View Entry ${id === ids[0] ? "A" : "B"} details"]`,
  );
const panel = () => document.querySelector('[data-testid="entry-preview"]');
const close = () =>
  document.querySelector('button[aria-label="Close entry details"]');
const button = (name) =>
  [...document.querySelectorAll("button")].find(
    (x) => x.textContent.trim() === name,
  );
const frame = (window) =>
  React.act(
    async () => new Promise((resolve) => window.requestAnimationFrame(resolve)),
  );

async function fixture(
  render,
  window,
  customRead,
  originalRead,
  initialPath = "/transactions",
  workspaceRead,
  workspaceState = state,
) {
  window.history.replaceState(null, "", initialPath);
  const app = await loadApplication();
  const original = globalThis.fetch;
  const writes = [];
  const reads = [];
  const pending = [];
  const originals = [];
  globalThis.fetch = async (url, options) => {
    const parsed = new URL(url, "http://localhost");
    if (workspaceRead && parsed.pathname.startsWith("/api/v1/financial-reports")) return workspaceRead(parsed);
    if (parsed.pathname.endsWith("/session"))
      return response({ csrf: "fixture" });
    if (parsed.pathname === "/api/v1/documents") return response([]);
    if (parsed.pathname === "/api/v1/sources")
      return response({ sources: [], has_more: false });
    if (/^\/api\/v1\/transactions\/[^/]+\/documents$/.test(parsed.pathname))
      return response([]);
    if (parsed.pathname.startsWith("/api/v1/receipts/")) {
      originals.push({ url: parsed, options });
      if (!originalRead) throw new Error("Unexpected original read");
      return originalRead(parsed, options);
    }
    if (options?.method === "POST") {
      writes.push(JSON.parse(options.body));
      return new Promise((resolve) => pending.push(resolve));
    }
    const id = parsed.pathname.split("/").at(-1);
    if (ids.includes(id)) {
      assert.equal(parsed.searchParams.get("book_id"), book.id);
      reads.push(id);
      return customRead ? customRead(id) : response(detail(id));
    }
    return response(workspaceState);
  };
  await render(
    React.createElement(
      app.Theme,
      { initialTheme: "light", initialPreference: "light" },
      React.createElement(app.BookWorkspace, {
        books: [book, other],
        selectedBook: book,
        creationKey: "fixture",
        focusHeading: false,
        onSelect() {},
        onCreated() {},
        onBooksChanged() {},
        initialState: workspaceState,
      }),
    ),
  );
  return {
    writes,
    reads,
    pending,
    originals,
    restore: () => {
      globalThis.fetch = original;
    },
  };
}

test("the inspector leads with human context and keeps original notes and accounting behind deliberate disclosures", async () => {
  await withDOM(async ({ render, window }) => {
    const originalNote = `Synthetic import trace; SHA256 ${"a".repeat(64)}; /private/fixture/statement.csv`;
    const value = {
      ...detail(ids[0]),
      kind: "income",
      counterparty: "Wise",
      description: "Wise interest",
      date: "2026-10-01",
      amount_minor: "66",
      note: originalNote,
      fx: {
        ...entries[0].fx,
        original_amount_minor: "66",
        paid_amount_minor: "66",
        cash_account: "synthetic-bank",
      },
      lines: [
        {
          id: "a",
          account_code: "synthetic-bank",
          account_name: "Wise USD",
          side: "debit",
          amount_minor: "66",
          native_currency: "USD",
          native_amount_minor: "66",
        },
        {
          id: "b",
          account_code: "synthetic-interest",
          account_name: "Interest earned",
          side: "credit",
          amount_minor: "66",
        },
      ],
    };
    const f = await fixture(render, window, () => response(value));
    try {
      await React.act(async () => action(ids[0]).click());
      const overview = panel().querySelector(
        'section[aria-label="Transaction summary"]',
      );
      assert.ok(overview.querySelector(".entry-hero [data-account-avatar]"), "the party leads with its avatar");
      assert.match(
        overview.textContent,
        /Wise.*Wise interest.*\$0.66.*Received.*October 1, 2026/s,
      );
      assert.match(overview.textContent, /CategoryInterest earned/);
      assert.equal(
        (overview.textContent.match(/\$0\.66/g) || []).length,
        1,
        "the same-currency amount has one primary presentation",
      );
      assert.doesNotMatch(
        overview.textContent,
        /Recorded at|Entry ID|SHA256|\/private\/|synthetic-interest|Actual amount/,
      );
      const disclosures = [...panel().querySelectorAll("details")];
      const accounting = panel().querySelector('section[aria-label="Postings"]');
      const record = disclosures.find((node) =>
        node.querySelector("summary")?.textContent.includes("Record details"),
      );
      assert.ok(accounting);
      assert.equal(accounting.closest("details"), null);
      assert.equal(accounting.querySelectorAll(".entry-posting").length, 2);
      assert.equal(record.open, false);
      assert.ok(
        record.textContent.includes(originalNote),
        "the exact original note remains available without interpreting or rewriting it",
      );
      assert.doesNotMatch(
        accounting.textContent,
        /synthetic-bank|synthetic-interest|native/,
      );
      assert.match(accounting.textContent, /From.*Interest earned.*To.*Wise USD/s);
      assert.equal(
        panel().querySelector('a[target="_blank"]'),
        null,
        "notes cannot fabricate a source link",
      );
      assert.equal(f.writes.length, 0);
    } finally {
      f.restore();
    }
  });
});

test("row inspection is nonmodal, retains table/card/selection, follows history and returns focus without scrolling", async () => {
  await withDOM(async ({ render, window }) => {
    const f = await fixture(render, window);
    try {
      const table = document.querySelector('table[aria-label="Transactions"]');
      const a = action(ids[0]),
        b = action(ids[1]);
      const selected = table.querySelector(
        'input[aria-label="Select Entry A"]',
      );
      await React.act(async () => selected.click());
      await React.act(async () => {
        a.focus();
        a.click();
      });
      const card = panel();
      assert.ok(card);
      assert.equal(document.querySelector(".dialog-overlay"), null);
      assert.equal(document.querySelector('[role="dialog"]'), null);
      assert.match(card.textContent, /Entry A/);
      assert.equal(
        new URLSearchParams(window.location.search).get("entryBook"),
        book.id,
      );
      assert.ok(
        !document.querySelector('button[aria-label^="Switch Book"]').disabled,
        "a read-only preview does not lock book navigation",
      );
      const body = card.querySelector(".entry-preview-content").parentElement;
      body.scrollTop = 80;
      await React.act(async () => {
        b.focus();
        b.click();
      });
      assert.equal(
        panel(),
        card,
        "switching records retains the same preview surface",
      );
      assert.equal(
        document.querySelector('table[aria-label="Transactions"]'),
        table,
      );
      assert.ok(selected.checked, "selection is independent of inspection");
      assert.match(card.textContent, /Entry B/);
      assert.equal(body.scrollTop, 0, "new record starts at its top");
      assert.equal(b.getAttribute("aria-controls"), card.id);
      assert.equal(b.getAttribute("aria-current"), "true");
      await React.act(async () => {
        window.history.replaceState(
          null,
          "",
          `/transactions?entryBook=${book.id}&entryPreview=${ids[0]}`,
        );
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(panel(), card);
      assert.match(card.textContent, /Entry A/);
      const focusCalls = [];
      const originalFocus = window.HTMLElement.prototype.focus;
      window.HTMLElement.prototype.focus = function (options) {
        focusCalls.push({
          tag: this.tagName,
          label: this.getAttribute("aria-label"),
          row: this.closest("tr")?.getAttribute("data-record-id"),
          options,
        });
        return originalFocus.call(this, options);
      };
      await React.act(async () => close().click());
      await frame(window);
      window.HTMLElement.prototype.focus = originalFocus;
      assert.ok(focusCalls.length > 0);
      assert.ok(
        focusCalls.every((call) => call.options?.preventScroll === true),
        "focus restoration must not scroll the table",
      );
      assert.ok(
        panel().querySelector(".collection-inspector-body[data-empty]"),
        "the docked inspector returns to its idle state",
      );
      assert.ok(
        document.activeElement === action(ids[1]) ||
          document.activeElement === action(ids[1]).closest("tr"),
        "close returns to the originating row or its detail button",
      );
      assert.equal(
        new URLSearchParams(window.location.search).get("entryPreview"),
        null,
      );
      const count = f.reads.length;
      await React.act(async () => {
        window.history.replaceState(
          null,
          "",
          `/transactions?entryBook=${other.id}&entryPreview=${ids[0]}`,
        );
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.ok(
        panel().querySelector(".collection-inspector-body[data-empty]"),
        "the docked inspector returns to its idle state",
      );
      assert.equal(
        f.reads.length,
        count,
        "a preview from a different book cannot become a read in this Book",
      );
    } finally {
      f.restore();
    }
  });
});

test("compact inspection replaces the list, preserves selection and scroll, and adapts to workspace resizing", async () => {
  await withDOM(async ({ render, window }) => {
    const size = inspectorSize(window, 360);
    const f = await fixture(render, window);
    try {
      const table = document.querySelector('[data-testid="book-transactions"]');
      const list = table.closest(".collection-inspector-collection");
      const card = panel();
      assert.ok(card.hidden);
      assert.ok(!list.hidden);
      const scroll = table.querySelector("[data-vult-view-scroll]");
      scroll.scrollTop = 140;
      const selection = table.querySelector(
        'input[aria-label="Select Entry A"]',
      );
      await React.act(async () => selection.click());
      await React.act(async () => action(ids[0]).click());
      assert.ok(list.hidden && list.hasAttribute("inert"));
      assert.ok(!card.hidden);
      assert.equal(document.activeElement, card.querySelector("h2"));
      assert.equal(
        document.querySelector('[data-testid="book-transactions"]'),
        table,
      );
      await React.act(async () => size.set(1280));
      assert.ok(
        !list.hidden && !card.hidden,
        "wide workspaces dock both panes",
      );
      assert.equal(panel(), card);
      await React.act(async () => size.set(360));
      assert.ok(list.hidden);
      const back = card.querySelector(
        'button[aria-label="Back to transactions"]',
      );
      assert.ok(back && !back.disabled);
      const nativeFocus = window.HTMLElement.prototype.focus;
      window.HTMLElement.prototype.focus = function (options) {
        nativeFocus.call(this, options);
        if (this.matches("tr[data-record-id]"))
          queueMicrotask(() => {
            queueMicrotask(() => {
              scroll.scrollLeft = 34;
              scroll.scrollTop = 999;
              scroll.dispatchEvent(new window.Event("scroll"));
            });
          });
      };
      try {
        await React.act(async () => back.click());
        await frame(window);
      } finally {
        window.HTMLElement.prototype.focus = nativeFocus;
      }
      assert.ok(card.hidden && !list.hidden && !list.hasAttribute("inert"));
      assert.ok(selection.checked);
      assert.equal(scroll.scrollTop, 140);
      assert.equal(
        scroll.scrollLeft,
        0,
        "focus-driven grid scrolling must not move the restored list",
      );
      assert.ok(
        document.activeElement === action(ids[0]) ||
          document.activeElement === action(ids[0]).closest("tr"),
      );
      scroll.scrollTop = 280;
      await React.act(async () => action(ids[0]).click());
      assert.ok(list.hidden);
      // Hidden layouts can reset DOM scroll metrics. Reopening the same row
      // must create a fresh bookmark and keep it through subsequent renders.
      scroll.scrollTop = 0;
      await React.act(async () => size.set(1280));
      await React.act(async () => size.set(360));
      await React.act(async () =>
        card.querySelector('button[aria-label="Back to transactions"]').click(),
      );
      await frame(window);
      assert.equal(scroll.scrollTop, 280);
      assert.ok(selection.checked);
      assert.equal(f.writes.length, 0);
    } finally {
      f.restore();
      size.restore();
    }
  });
});

test("Sources has independent navigation and preserves the retained transaction collection", async () => {
  await withDOM(async ({ render, window }) => {
    const f = await fixture(render, window);
    try {
      const transactions = document.querySelector(
        '[data-testid="book-transactions"]',
      );
      assert.equal(document.querySelector('nav a[href="/documents"]'), null);
      assert.equal(
        document.querySelector('nav[aria-label="Activity sections"]'),
        null,
      );
      const sources = document.querySelector(
        'nav[aria-label="Book navigation"] a[href="/sources"]',
      );
      assert.equal(sources.textContent, "Sources");
      assert.equal(document.querySelector('button[aria-label="Manage transactions"]'), null, "agents record entries; Activity has no entry menu");
      await React.act(async () => sources.click());
      assert.equal(window.location.pathname, "/sources");
      assert.equal(sources.getAttribute("aria-current"), "page");
      assert.equal(
        document
          .querySelector(
            'nav[aria-label="Book navigation"] a[href="/transactions"], nav[aria-label="Book navigation"] a[href="/activity"]',
          )
          .hasAttribute("aria-current"),
        false,
      );
      assert.ok(document.querySelector('[data-testid="book-documents"]'));
      assert.equal(
        document.querySelector('nav[aria-label="Breadcrumb"]').textContent,
        "Sources",
      );
      assert.equal(
        document
          .querySelector('table[aria-label="Sources"]')
          .closest("[hidden], [inert]"),
        null,
      );
      const activity = document.querySelector(
        'nav[aria-label="Book navigation"] a[href="/transactions"], nav[aria-label="Book navigation"] a[href="/activity"]',
      );
      await React.act(async () => activity.click());
      assert.equal(
        document.querySelector('[data-testid="book-transactions"]'),
        transactions,
      );
      await React.act(async () => action(ids[0]).click());
      assert.ok(panel(), "Entry inspection is still available from Activity");
      assert.equal(f.writes.length, 0);
    } finally {
      f.restore();
    }
  });
});

for (const path of [
  "/sources?kind=expense",
  "/documents?kind=expense",
  "/?kind=expense#documents",
]) {
  test(`Sources accepts ${path} without losing the Book filters`, async () => {
    await withDOM(async ({ render, window }) => {
      const f = await fixture(render, window, undefined, undefined, path);
      try {
        assert.equal(window.location.pathname, "/sources");
        assert.equal(
          new URLSearchParams(window.location.search).get("kind"),
          "expense",
        );
        assert.equal(
          document.querySelector(
            'nav[aria-label="Book navigation"] a[aria-current="page"]',
          ).textContent,
          "Sources",
        );
        assert.equal(
          document
            .querySelector('table[aria-label="Sources"]')
            .closest("[hidden], [inert]"),
          null,
        );
        assert.equal(f.writes.length, 0);
      } finally {
        f.restore();
      }
    });
  });
}

test("older Book-scoped preview links open in the visible Activity workbench", async () => {
  await withDOM(async ({ render, window }) => {
    const f = await fixture(render, window);
    try {
      await React.act(async () => {
        window.history.replaceState(
          null,
          "",
          `/dashboard?entryBook=${book.id}&entryPreview=${ids[0]}`,
        );
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(window.location.pathname, "/activity");
      assert.equal(
        document
          .querySelector('[data-testid="book-transactions"]')
          .closest(".workspace-pane").hidden,
        false,
      );
      assert.match(panel().textContent, /Entry A/);
      assert.equal(f.writes.length, 0);
    } finally {
      f.restore();
    }
  });
});

test("a late detail response cannot overwrite the next selected record", async () => {
  await withDOM(async ({ render, window }) => {
    let finish;
    const f = await fixture(render, window, (id) =>
      id === ids[0]
        ? new Promise((resolve) => {
            finish = resolve;
          })
        : response(detail(id)),
    );
    try {
      await React.act(async () => action(ids[0]).click());
      const card = panel();
      assert.match(card.textContent, /Loading entry/);
      await React.act(async () => action(ids[1]).click());
      assert.equal(panel(), card);
      assert.match(card.textContent, /Entry B/);
      await React.act(async () => finish(response(detail(ids[0]))));
      assert.equal(panel(), card);
      assert.match(card.textContent, /Entry B/);
      assert.doesNotMatch(card.textContent, /Entry A/);
    } finally {
      f.restore();
    }
  });
});

test("Statements entry investigation keeps the captured report, expanded account and contribution dock through close and Back",()=>withDOM(async({window,render})=>{
  let reportReads=0,contributionReads=0;
  const report={schema_version:1,report:"balance_sheet",title:"Balance Sheet",book:{...book,fiscal_year_start_month:1,fiscal_year_start_day:1,recognition_basis:"unknown",reporting_framework:"unknown"},request:{book_id:book.id,report:"balance_sheet",as_of:"2026-10-31",currency:"USD",comparison:"none"},scope:{mode:"live",period:{to:"2026-10-31"},fiscal_start:"2026-01-01",ledger_revision:"synthetic-r1",knowledge_cutoff:"2026-11-01T00:00:00Z",mapping_version:"synthetic-mapping",hierarchy_basis:"flat_current_catalog",generated_at:"2026-11-01T00:00:00Z",population_digest:"b".repeat(64),coverage:"unknown",opening_balances:"unknown"},columns:[{id:"current",label:"Current",kind:"amount",period:{to:"2026-10-31"}}],rows:[{id:"assets",label:"Assets",role:"section",values:{current:"11000"},children:[{id:"cash",label:"Business cash",role:"account",values:{current:"11000"},children:[],drilldown:{account_ids:["cash"],to:"2026-10-31",exclude_closing:false,exclude_opening:false}}]}],checks:[],limitations:["Source coverage unconfirmed"],reconciliation:{},status:"partial",digest:"a".repeat(64),export_csv:"Account,Current\r\nCash,110.00\r\n"};
  const f=await fixture(render,window,undefined,undefined,"/statements?report=balance_sheet&from=2026-10-01&to=2026-10-31&as_of=2026-10-31&compare=none",(url)=>{
    if(url.pathname.endsWith("/contributions")){contributionReads++;return response({result_digest:report.digest,row_id:"cash",column_id:"current",debit_minor:"11000",credit_minor:"0",total_minor:"11000",contributions:[{posting_id:"p1",entry_id:ids[0],account_id:"cash",date:"2026-10-12",side:"debit",amount_minor:"11000",signed_amount_minor:"11000",report_amount_minor:"11000",column_id:"current",purpose:"ordinary"}]});}
    reportReads++;return response(report);
  });
  const settle=()=>React.act(async()=>new Promise(resolve=>setTimeout(resolve,90)));
  try{
    await settle();
    const inspect=document.querySelector('button[data-grid-row="cash"][data-grid-column="current"]');assert.ok(inspect,"report line is selectable and inspectable: " + document.body.textContent);
    await React.act(()=>{inspect.click();inspect.dispatchEvent(new window.KeyboardEvent("keydown",{key:"Enter",bubbles:true}));});await settle();
    const dock=document.querySelector('.statement-inspector');assert.ok(dock);
    const entry=document.querySelector('button[aria-label="Inspect contributing entry 1"]');assert.ok(entry);
    const main=document.getElementById('main-content');main.scrollTop=530;const href=window.location.href;
    await React.act(()=>{entry.focus();entry.click();});await settle();
    assert.equal(window.location.pathname,"/statements");assert.equal(document.querySelector('.statement-inspector'),dock);assert.ok(document.querySelector('.entry-context-dialog[role="dialog"]'));
    const close=document.querySelector('.entry-context-dialog button[aria-label="Close dialog"]');assert.ok(close);await React.act(()=>close.click());await settle();
    assert.equal(window.location.href,href);assert.equal(document.querySelector('.statement-inspector'),dock);assert.equal(reportReads,1);assert.equal(contributionReads,1);
    await React.act(()=>{entry.focus();entry.click();});await settle();await React.act(async()=>{window.history.back();await new Promise(resolve=>setTimeout(resolve,90));});
    assert.equal(window.location.href,href);assert.equal(document.querySelector('.statement-inspector'),dock);assert.equal(reportReads,1);assert.equal(f.writes.length,0);
  }finally{f.restore();}
}));


test("Accounts contextual entry links remain in the actual Accounts host through startup and close",()=>withDOM(async({window,render})=>{
 const period = { from: "2026-10-01", to: "2026-10-31" };
 const accountsState = {
  ...state, read_view: "accounts", period,
  // A Book with no accounts shows the starter charts instead of the list.
  account_balances: [{ id: "00000000-0000-4000-8000-000000000001", code: "00000000-0000-4000-8000-000000000001", name: "Example cash", kind: "asset", usage: "general", revision: 1, currency: "", scale: 2, native_status: "not_recorded", native_minor: null, balance_minor: "0", period_change_minor: "0", posting_count: 0, through_sequence: 0 }],
  position: { assets_minor: "0", liabilities_minor: "0", equity_accounts_minor: "0", accumulated_result_minor: "0", net_assets_minor: "0" },
  summary: { income_minor: "0", expenses_minor: "0", net_minor: "0" },
  account_activity: {
   version: 1, book_id: book.id, currency: book.currency, scale: book.scale, as_of: period.to,
   from: "2025-11-02", accounts: {}, empty_weekly: Array(52).fill(0), groups: [],
   content_sha256: "a".repeat(64), native_balances: {}, native_currencies: {},
  },
 };
 const f = await fixture(render, window, undefined, undefined,
  `/accounts?from=${period.from}&to=${period.to}&entryPreview=${ids[0]}&entryBook=${book.id}`,
  undefined, accountsState);
 const settle=()=>React.act(async()=>new Promise(resolve=>setTimeout(resolve,200)));
 try{
  await settle();
  assert.equal(window.location.pathname,"/accounts");
  assert.ok(document.querySelector('.entry-context-dialog[role="dialog"][data-state="open"]'),document.body.textContent.slice(-1800));
  assert.match(document.querySelector('.entry-context-dialog').textContent,/Entry A/);
  const close=document.querySelector('.entry-context-dialog button[aria-label="Close dialog"]');assert.ok(close);
  await React.act(()=>close.click());await settle();
  assert.equal(window.location.pathname,"/accounts");
  assert.equal(new URL(window.location.href).searchParams.get("range"), "2026-10");
  assert.equal(f.writes.length,0);
 }finally{f.restore();}
}));
