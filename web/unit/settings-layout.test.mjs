import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { createRequire } from "node:module";
import { build } from "esbuild";
import { React, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

const bundle = await build({
  stdin: {
    contents:
      'export { Settings } from "./components/workspaces/settings"; export { Theme } from "./design-system/theme";',
    resolveDir: new URL("../src", import.meta.url).pathname,
    loader: "tsx",
  },
  jsx: "automatic",
  bundle: true,
  write: false,
  platform: "node",
  format: "cjs",
  packages: "external", plugins: [phosphorPlugin],
  loader: { ".module.css": "empty", ".css": "empty" },
});
let components;
function settingsComponents() {
  // Portals select their browser layout hook when the module loads.
  if (!components) {
    const loaded = { exports: {} };
    new Function("module", "exports", "require", bundle.outputFiles[0].text)(
      loaded,
      loaded.exports,
      createRequire(import.meta.url),
    );
    components = loaded.exports;
  }
  return components;
}
const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic layout book",
  founder: "Synthetic owner",
  profile: "business",
  currency: "USD",
  scale: 2,
  start_date: "2024-12-13",
  fiscal_year_start_month: 4,
  fiscal_year_start_day: 6,
  accounting_timezone: "Europe/Warsaw",
};
const section = (name) =>
  [...document.querySelectorAll(".vult-settings-section")].find(
    (node) => node.querySelector("h2")?.textContent === name,
  );
const row = (name, scope = document) =>
  [...scope.querySelectorAll(".vult-settings-row")].find(
    (node) =>
      node.querySelector(".vult-settings-row-intro > :first-child")
        ?.textContent === name,
  );
const save = (name) =>
  section(name)?.querySelector(".vult-settings-section-actions button");
const button = (name) =>
  [...document.querySelectorAll("button")].find(
    (node) => node.textContent.trim() === name,
  );
const click = async (node) => {
  assert.ok(node, "Expected the existing control to remain reachable");
  await React.act(async () => node.click());
};

const tab = (name) =>
  [...document.querySelectorAll('[role="tab"]')].find(
    (node) => node.textContent.trim() === name,
  );
const selectTab = async (name) => {
  await click(tab(name));
  assert.equal(tab(name).getAttribute("aria-selected"), "true");
  const panel = document.querySelector('[role="tabpanel"]');
  assert.ok(panel);
  assert.equal(panel.getAttribute("aria-labelledby"), tab(name).id);
  return panel;
};

async function fixture(
  render,
  { mode = "local", ...props } = {},
) {
  const previous = globalThis.fetch;
  const reads = [];
  const changes = [];
  let tokens = [
    {
      id: "existing-token",
      name: "Existing agent",
      connection: true,
      expires_at: null,
      book_grants: [{ book_id: book.id, role: "view", rights: ["read"] }],
    },
  ];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url, "http://localhost").pathname;
    const method = init.method || "GET";
    reads.push({ path, method });
    const result = (data, ok = true) => ({
      ok,
      status: ok ? 200 : 503,
      json: async () => data,
    });
    if (path === "/auth/session")
      return result({
        mode,
        csrf: "synthetic-csrf",
      });
    if (path === "/api/v1/legal-identity-catalog")
      return result({
        catalog: {
          version: "synthetic-1",
          countries: [],
          jurisdictions: [],
          forms: [],
          overlays: [],
          tax_classifications: [],
        },
      });
    if (path === "/api/v1/book-identity" && method === "POST") {
      changes.push({ path, body: JSON.parse(init.body), csrf: init.headers["X-CSRF-Token"] });
      return result({ schema_version: 1 });
    }
    if (path === "/api/v1/book-identity" && method === "GET")
      return result({
        subject: {
          id: "synthetic-subject",
          revision: 0,
          kind: null,
          identity: {},
        },
        history: [],
        has_more: false,
      });
    if (path === "/api/v1/state") return result({ transactions: [] });
    if (path === "/api/v1/opening-entries")
      return result({
        book_id: props.book?.id || book.id,
        currency: "USD",
        scale: 2,
        transactions: [],
        has_more: false,
        next_offset: 0,
      });
    if (path === "/api/v1/settings" && method === "POST") {
      changes.push({
        path,
        body: JSON.parse(init.body),
        csrf: init.headers["X-CSRF-Token"],
      });
      const sent = changes.at(-1).body;
      // The server answers with the accepted Book at its next revision.
      return props.saveResponse
        ? props.saveResponse(sent)
        : result({ id: sent.book_id, name: sent.name, founder: sent.founder, currency: "USD", scale: 2, metadata_revision: sent.revision + 1 });
    }
    if (path === "/api/v1/access/tokens" && method === "GET")
      return result({ tokens });
    if (
      path === "/api/v1/access/tokens/existing-token/revoke" &&
      method === "POST"
    ) {
      changes.push({
        path,
        body: JSON.parse(init.body),
        csrf: init.headers["X-CSRF-Token"],
      });
      tokens = tokens.map((token) =>
        token.id === "existing-token"
          ? { ...token, revoked_at: "2026-10-26T12:00:00Z" }
          : token,
      );
      return result({});
    }
    throw new Error(`Unexpected request ${method} ${path}`);
  };
  const { Settings, Theme } = settingsComponents();
  const mount = (next = {}) =>
    render(
      React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "system" },
        React.createElement(Settings, {
          key: next.book?.id || book.id,
          book,
          displayBook: book,
          state: {
            summary: {
              cash_minor: "0",
              income_minor: "0",
              expenses_minor: "0",
              owed_minor: "0",
              reimbursements_minor: "0",
              loans_minor: "0",
            },
            cash_accounts: [],
            payables: [],
            fx_gain_minor: "0",
            fx_loss_minor: "0",
          },
          onSaved() {},
          onDetail() {},
          ...props,
          ...next,
        }),
      ),
    );
  await mount();
  return {
    mount,
    reads,
    changes,
    restore: () => {
      globalThis.fetch = previous;
    },
  };
}

test("every Book field stays in the settings row layout, including fiscal year and timezone, with all scoped originals and actions", async () => {
  await withDOM(async ({ render, window }) => {
    const addressedBook = {
      ...book,
      address: {
        book_id: book.id,
        code: "K7FM",
        slug: "synthetic-layout-book",
        revision: 0,
        aliases: ["k7fm", "synthetic-layout-book"],
      },
    };
    const f = await fixture(render, {
      book: addressedBook,
      displayBook: addressedBook,
      sourceImport: { transactions: 27, documents: 9, through: "2026-10-20" },
    });
    try {
      const details = section("Name");
      const identifier = section("Address");
      const fields = ["Book name"];
      assert.deepEqual(
        [
          ...details
            .closest('[role="tabpanel"]')
            .querySelectorAll(".vult-settings-section > header h2"),
        ].map((node) => node.textContent),
        ["Logo", "Name", "Address"],
      );
      assert.equal(row("Accounting identity", details), undefined);
      assert.equal(row("Founder display name"), undefined);
      assert.equal(section("Book URL"), undefined);
      assert.match(row("Book ID", identifier).textContent, /K7FM/);
      const copied = [];
      Object.defineProperty(window.navigator, "clipboard", {
        configurable: true,
        value: { writeText: async (value) => copied.push(value) },
      });
      await click(
        identifier.querySelector('button[aria-label="Copy Book ID"]'),
      );
      assert.deepEqual(copied, ["K7FM"]);
      assert.deepEqual(
        f.changes,
        [],
        "Identifier copy cannot submit either General form",
      );
      assert.equal(
        identifier.querySelector('input[aria-label="Book URL"]').value,
        "synthetic-layout-book",
      );
      assert.equal(details.querySelectorAll("form").length, 1);
      assert.equal(identifier.querySelectorAll("form").length, 1);
      assert.equal(document.querySelector("form form"), null);
      assert.equal(
        details
          .querySelector(".book-settings-details-form")
          .elements.namedItem("book-slug"),
        null,
      );
      assert.equal(
        identifier.querySelector(".book-url-form").elements.namedItem("name"),
        null,
      );
      const generalPanel = details.closest('[role="tabpanel"]');
      await selectTab("Accounting");
      const accounting = section("Book setup");
      assert.deepEqual(
        [
          ...accounting.querySelectorAll(
            ".vult-settings-row-intro > :first-child",
          ),
        ].map((node) => node.textContent),
        [
          "Book type",
          "Accounting currency",
          "Fiscal year starts",
          "Accounting timezone",
        ],
      );
      assert.equal(generalPanel.contains(section("Legal identity")), false);
      assert.match(
        accounting.querySelector(".vult-settings-description").textContent,
        /Recorded when this Book was created/,
      );
      assert.equal(accounting.querySelector("input,select,button"), null);
      assert.equal(row("Book ID", identifier).querySelector("input,select"), null);
      assert.match(
        row("Book ID", identifier).querySelector(".vult-settings-description").textContent,
        /Generated when the Book was created.*can’t be changed/,
      );
      assert.equal(row("Book ID", details), undefined);
      assert.equal(section("Opening balances"), undefined, "agents record opening entries");
      await selectTab("Legal identity");
      assert.equal(section("Legal identity").querySelector("form"), null);
      assert.equal(
        section("Legal identity").querySelector(".identity-history"),
        null,
      );
      await click(button("Add details"));
      assert.deepEqual(
        [
          ...section("Legal identity").querySelectorAll(
            ".book-settings-identity-group > h3",
          ),
        ].map((node) => node.textContent),
        ["Basic details", "Country and business structure", "Change details"],
      );
      assert.ok(
        section("Legal identity").querySelector(
          'select[aria-label="Identity type"]',
        ),
      );
      await selectTab("General");
      assert.deepEqual(
        [
          ...details.querySelectorAll(
            ".vult-settings-row-intro > :first-child",
          ),
        ].map((p) => p.textContent),
        fields,
      );
      assert.deepEqual(
        [
          ...identifier.querySelectorAll(
            ".vult-settings-row-intro > :first-child",
          ),
        ].map((p) => p.textContent),
        ["Book URL", "Book ID"],
      );
      assert.match(
        row("Fiscal year starts", accounting).querySelector(
          ".vult-settings-row-content",
        ).textContent,
        /April 6/,
      );
      assert.match(
        row("Accounting timezone", accounting).querySelector(
          ".vult-settings-row-content",
        ).textContent,
        /Europe\/Warsaw/,
      );
      assert.equal(row("Book start date", accounting), undefined);
      assert.equal(details.querySelectorAll(".vult-settings-row").length, 1);
      assert.equal(document.querySelectorAll(".workspace-surface").length, 0);
      assert.equal(
        document.querySelector('[aria-label="Account and agent access"]'),
        null,
        "Local mode retains the hosted-access boundary",
      );
      await selectTab("Your data");
      await click(button("Download"));
      const exports = [...document.querySelectorAll('a[href*="/exports/"]')];
      // The same files the terminal writes with `vult export`.
      assert.deepEqual(
        exports.map((a) => new URL(a.href).pathname),
        [
          "book.tar",
          "journal.json",
          "transactions.csv",
          "journal-lines.csv",
          "accounts.csv",
        ].map((file) => `/api/v1/exports/${file}`),
      );
      for (const link of exports)
        assert.equal(new URL(link.href).searchParams.get("book_id"), book.id);
      assert.equal(document.querySelector('a[href*="source-package"]'), null);
      assert.ok(![...document.querySelectorAll("h2, h3")].some((h) => /migration/i.test(h.textContent)));
      assert.equal(section("Book balances"), undefined);
      assert.doesNotMatch(
        document.querySelector(".book-settings-page").textContent,
        /Recorded cash|Recorded income|Recorded expenses|Imported transactions|Records through|Money owed by currency|Currency exchange:|Import from [A-Z]/,
      );
      await React.act(async () =>
        document.querySelector('[role="menu"]').dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "Escape",
            bubbles: true,
            cancelable: true,
          }),
        ),
      );
      await selectTab("General");
      assert.equal(button("Edit details"), undefined);
      assert.equal(document.querySelector('[role="dialog"]'), null);
      assert.equal(
        document.querySelector('input[name="name"]').value,
        book.name,
      );
      assert.equal(document.querySelector('input[name="founder"]'), null);
      assert.ok(save("Name"));
      assert.ok(![...document.querySelectorAll("button")].some((b) => /^Import from /.test(b.textContent.trim())));
      await selectTab("Your data");
      const backup = document.querySelector('a[href*="exports/book.tar"]');
      assert.ok(backup);
      assert.match(backup.textContent, /Download Book/);
      assert.doesNotMatch(
        section("Download your data").textContent,
        /PostgreSQL|scripts\/backup/,
      );
      assert.deepEqual(f.changes, []);
    } finally {
      f.restore();
    }
  });
});

test("Settings follows the heading/subtitle/tab layout with keyboard navigation and retained Book drafts", async () => {
  await withDOM(async ({ render, window }) => {
    const routed = {
      ...book,
      address: {
        book_id: book.id,
        code: "K7FM",
        slug: "synthetic-layout-book",
        revision: 0,
        aliases: ["k7fm", "synthetic-layout-book"],
      },
    };
    const f = await fixture(render, { book: routed, displayBook: routed });
    try {
      // The page bar names Settings; the page starts with its tabs.
      assert.equal(document.querySelector(".book-settings-page-header"), null);
      assert.equal(
        document.querySelector(".book-settings-page h1, .book-settings-page h2")
          .textContent,
        "Logo",
      );
      assert.deepEqual(
        [...document.querySelectorAll('[role="tab"]')].map((node) =>
          node.textContent.trim(),
        ),
        ["General", "Accounting", "Legal identity", "Your data", "Connections"],
      );
      assert.equal(tab("General").getAttribute("aria-selected"), "true");
      assert.equal(document.querySelectorAll('[role="tabpanel"]').length, 1);
      const form = section("Name").querySelector("form");
      form.elements.namedItem("name").value = "Retain this unsaved Book draft";
      const slug = section("Address").querySelector('input[name="book-slug"]');
      await React.act(async () => {
        Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype,
          "value",
        ).set.call(slug, "retained-url-draft");
        slug.dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      const reads = f.reads.length;
      assert.equal(
        f.reads.some((read) => read.path === "/api/v1/book-identity"),
        false,
        "identity does not load before its first visit",
      );
      await React.act(async () => {
        tab("General").focus();
        tab("General").dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "ArrowRight",
            bubbles: true,
          }),
        );
      });
      assert.equal(tab("Accounting").getAttribute("aria-selected"), "true");
      await React.act(async () => {
        tab("Accounting").dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "ArrowRight",
            bubbles: true,
          }),
        );
      });
      assert.equal(tab("Legal identity").getAttribute("aria-selected"), "true");
      await click(button("Add details"));
      const identityForm = section("Legal identity").querySelector("form");
      identityForm.elements.namedItem("reason").value =
        "Retain this unsaved identity change";
      await React.act(async () => {
        tab("Legal identity").dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "ArrowRight",
            bubbles: true,
          }),
        );
      });
      assert.equal(tab("Your data").getAttribute("aria-selected"), "true");
      assert.ok(
        form.closest("[data-inert]"),
        "inactive Book fields are retained and cannot receive focus",
      );
      const data = await selectTab("Your data");
      assert.ok(data.contains(section("Download your data")));
      assert.equal(data.querySelectorAll('a[href*="/exports/"]').length, 1);
      await selectTab("General");
      assert.equal(section("Name").querySelector("form"), form);
      assert.equal(
        form.elements.namedItem("name").value,
        "Retain this unsaved Book draft",
      );
      assert.equal(
        section("Address").querySelector('input[name="book-slug"]'),
        slug,
      );
      assert.equal(slug.value, "retained-url-draft");
      await selectTab("Legal identity");
      assert.equal(
        section("Legal identity").querySelector("form"),
        identityForm,
      );
      assert.equal(
        identityForm.elements.namedItem("reason").value,
        "Retain this unsaved identity change",
      );
      await selectTab("General");
      assert.equal(
        f.reads.length,
        reads + 2,
        "identity loads once on first visit; revisiting tabs does not reread or reset mounted forms",
      );
      assert.deepEqual(f.changes, []);
    } finally {
      f.restore();
    }
  });
});

test("leaving Settings retains drafts and postpones hidden identity refresh until return", async () => {
  await withDOM(async ({ render }) => {
    const f = await fixture(render);
    try {
      const form = section("Name").querySelector("form");
      form.elements.namedItem("name").value = "Unsaved Book name";
      await selectTab("Legal identity");
      await click(button("Add details"));
      const identity = section("Legal identity").querySelector("form");
      identity.elements.namedItem("reason").value = "Unsaved identity reason";
      const count = () =>
        f.reads.filter((read) => read.path === "/api/v1/book-identity").length;
      assert.equal(count(), 1);
      await f.mount({ visible: false, refreshKey: 1 });
      await f.mount({ visible: false, refreshKey: 2 });
      assert.equal(count(), 1);
      await f.mount({ visible: true, refreshKey: 2 });
      assert.equal(count(), 2);
      assert.equal(section("Legal identity").querySelector("form"), identity);
      assert.equal(
        identity.elements.namedItem("reason").value,
        "Unsaved identity reason",
      );
      await selectTab("General");
      assert.equal(section("Name").querySelector("form"), form);
      assert.equal(form.elements.namedItem("name").value, "Unsaved Book name");
      assert.deepEqual(f.changes, []);
    } finally {
      f.restore();
    }
  });
});

test("an identity revision with a legal name omits an unset kind", async () => {
  await withDOM(async ({ render, window }) => {
    const f = await fixture(render);
    try {
      await selectTab("Legal identity");
      await click(button("Add details"));
      const form = section("Legal identity").querySelector("form");
      const name = form.elements.namedItem("legal_name");
      await React.act(async () => {
        Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(name, "Example legal name");
        name.dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      if (button("Review changes")) await click(button("Review changes"));
      form.elements.namedItem("reason").value = "Example identity revision";
      await React.act(async () => form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true })));
      assert.equal(f.changes.length, 1);
      assert.equal(f.changes[0].path, "/api/v1/book-identity");
      assert.equal(f.changes[0].body.identity.legal_name, "Example legal name");
      assert.equal(Object.hasOwn(f.changes[0].body, "kind"), false);
      assert.equal(f.changes[0].body.reason, "Example identity revision");
    } finally {
      f.restore();
    }
  });
});

test("Settings contains no personal Appearance controls", async () => {
  await withDOM(async ({ render }) => {
    const f = await fixture(render);
    try {
      assert.equal(tab("Preferences"), undefined);
      assert.equal(
        document.querySelector('select[aria-label="Theme preference"]'),
        null,
      );
      assert.ok(section("Name"));
      await selectTab("Your data");
      assert.equal(
        section("Download your data").querySelectorAll('a[href*="/exports/"]')
          .length,
        1,
      );
      assert.deepEqual(f.changes, []);
    } finally {
      f.restore();
    }
  });
});

test("legacy defaults, absent import actions and selected Book exports stay truthful on rerender", async () => {
  await withDOM(async ({ render }) => {
    const f = await fixture(render);
    try {
      assert.ok(![...document.querySelectorAll("h2, h3")].some((h) => /migration/i.test(h.textContent)));
      assert.ok(![...document.querySelectorAll("button")].some((b) => /^Import from /.test(b.textContent.trim())));
      const personal = { ...book, profile: "personal" };
      await f.mount({ book: personal, displayBook: personal });
      assert.equal(
        document.querySelector('input[aria-label="Owner display name"]'),
        null,
      );
      await selectTab("Accounting");
      assert.match(row("Book type").textContent, /Personal/);
      assert.equal(section("Opening balances"), undefined);
      const changed = {
        ...book,
        id: "22222222-2222-4222-8222-222222222222",
        name: "Second synthetic book",
        profile: undefined,
        fiscal_year_start_month: undefined,
        fiscal_year_start_day: undefined,
        accounting_timezone: undefined,
      };
      await f.mount({ book: changed, displayBook: changed });
      await selectTab("Accounting");
      assert.match(row("Book type").textContent, /Legacy · not classified/);
      // An unrecorded calendar is not January 1, and an unrecorded zone is not UTC.
      assert.match(row("Fiscal year starts").textContent, /Not recorded/);
      assert.match(row("Accounting timezone").textContent, /Not recorded/);
      await selectTab("General");
      assert.equal(
        row("Book name").querySelector("input").value,
        "Second synthetic book",
      );
      await selectTab("Your data");
      for (const a of document.querySelectorAll('a[href*="/exports/"]'))
        assert.equal(new URL(a.href).searchParams.get("book_id"), changed.id);
      assert.deepEqual(f.changes, []);
    } finally {
      f.restore();
    }
  });
});

for (const mode of ["local", "server"]) {
  test(`${mode} Settings lists Book access and confirms revocation`, async () => {
    await withDOM(async ({ render }) => {
      const f = await fixture(render, { mode });
      try {
        await selectTab("Connections");
        const list = section("Connected apps");
        assert.match(list.textContent, /Existing agent[\s\S]*View only/);
        assert.equal(list.querySelector("form"), null);
        await click(list.querySelector('button[aria-label^="Disconnect Existing agent"]'));
        const dialog = document.querySelector('[role="alertdialog"], [role="dialog"]');
        assert.ok(dialog);
        assert.match(dialog.textContent, /Disconnect Existing agent/);
        assert.equal(f.changes.length, 0);
        await click([...dialog.querySelectorAll("button")].find(node => node.textContent.trim() === "Disconnect"));
        assert.deepEqual(f.changes, [{
          path: "/api/v1/access/tokens/existing-token/revoke",
          body: {},
          csrf: "synthetic-csrf",
        }]);
        assert.match(list.textContent, /No agents connected/);
      } finally {
        f.restore();
      }
    });
  });
}

test("Book names are directly editable and one submission saves the existing Book-scoped payload in place", async () => {
  await withDOM(async ({ render, window }) => {
    let saved = 0;
    const f = await fixture(render, {
      onSaved() {
        saved++;
      },
    });
    try {
      const page = document.querySelector(".book-settings-page");
      const form = section("Name").querySelector("form");
      assert.ok(save("Name").form === form);
      assert.equal(button("Edit details"), undefined);
      assert.equal(form.elements.namedItem("founder"), null);
      form.elements.namedItem("name").value = "Updated synthetic name";
      await React.act(async () => {
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        );
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        );
      });
      assert.deepEqual(f.changes, [
        {
          path: "/api/v1/settings",
          body: {
            book_id: book.id,
            idempotency_key: f.changes[0]?.body.idempotency_key,
            revision: 0,
            name: "Updated synthetic name",
            // The recorded display name is sent unchanged.
            founder: book.founder,
          },
          csrf: "synthetic-csrf",
        },
      ]);
      assert.equal(saved, 1);
      assert.ok(document.querySelector(".book-settings-page") === page);
      assert.equal(document.querySelector('[role="dialog"]'), null);
      await selectTab("Accounting");
      for (const label of [
        "Book type",
        "Accounting currency",
        "Fiscal year starts",
        "Accounting timezone",
      ])
        assert.equal(row(label).querySelector("input,select"), null);
      assert.equal(
        section("Legal identity"),
        undefined,
        "unvisited identity is not mounted by a name save",
      );
      assert.equal(section("Book balances"), undefined);
      assert.equal(button("Record opening balance"), undefined);
    } finally {
      f.restore();
    }
  });
});

test("a save whose answer is lost freezes the change until it is checked or sent again unchanged", async () => {
  await withDOM(async ({ render, window }) => {
    let failed = true,
      saved = 0;
    const f = await fixture(render, {
      onSaved() {
        saved++;
      },
      saveResponse: async (sent) => ({
        ok: !failed,
        status: failed ? 503 : 200,
        json: async () =>
          failed
            ? { error: "Synthetic names save unavailable." }
            : { id: sent.book_id, name: sent.name, founder: sent.founder, currency: "USD", scale: 2, metadata_revision: 1 },
      }),
    });
    try {
      let form = section("Name").querySelector("form");
      form.elements.namedItem("name").value = "Retained names draft";
      await React.act(async () =>
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        ),
      );
      form = section("Name").querySelector("form");
      assert.match(
        section("Name").querySelector('[role="alert"]').textContent,
        /may already be saved/,
      );
      assert.equal(form.elements.namedItem("name").value, "Retained names draft");
      assert.equal(form.elements.namedItem("name").disabled, true, "an unresolved change cannot be edited and resent");
      assert.equal(save("Name").disabled, true);
      assert.ok(button("Discard change"));
      assert.equal(saved, 0);
      failed = false;
      const resend = [...section("Name").querySelectorAll("button")].find((b) => /^send again$/i.test(b.textContent.trim()));
      assert.ok(resend, "the notice offers to send the same change again");
      await React.act(async () => resend.click());
      await React.act(() => new Promise((done) => setTimeout(done, 0)));
      assert.equal(saved, 1);
      assert.equal(section("Name").querySelector('[role="alert"]'), null);
      assert.equal(f.changes.length, 2);
      assert.deepEqual(f.changes[0].body, f.changes[1].body, "the retry is the same request under the same key");
    } finally {
      f.restore();
    }
  });
});

test("a late names response from a departed Book cannot refresh or replace the next Book editor", async () => {
  await withDOM(async ({ render, window }) => {
    let settle,
      saved = 0;
    const f = await fixture(render, {
      onSaved() {
        saved++;
      },
      saveResponse: () =>
        new Promise((done) => {
          settle = done;
        }),
    });
    try {
      const form = section("Name").querySelector("form");
      form.elements.namedItem("name").value = "Old Book pending draft";
      await React.act(async () =>
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        ),
      );
      assert.equal(form.elements.namedItem("name").disabled, true);
      assert.equal(typeof settle, "function");
      const next = {
        ...book,
        id: "33333333-3333-4333-8333-333333333333",
        name: "Next synthetic book",
      };
      await f.mount({ book: next, displayBook: next });
      await React.act(async () => settle({ ok: true, json: async () => ({}) }));
      assert.equal(saved, 0);
      assert.equal(
        document.querySelector('input[name="name"]').value,
        next.name,
      );
      assert.equal(f.changes[0].body.book_id, book.id);
      assert.equal(save("Name").disabled, true);
    } finally {
      f.restore();
    }
  });
});

test("the real Settings route saves inline, refreshes the Book switcher and keeps the mounted page without opening a dialog", async () => {
  await withDOM(async ({ render, window }) => {
    const { App, Theme: AppTheme } = await loadApplication({
      workspace: "settings",
    });
    const oldFetch = globalThis.fetch;
    let current = { ...book };
    const changes = [];
    const currencies = [{ code: "USD", name: "US Dollar", scale: 2 }];
    const state = () => ({
      book: current,
      currencies,
      transactions: [],
      accounts: [],
      cash_accounts: [],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      summary: {
        cash_minor: "0",
        income_minor: "0",
        expenses_minor: "0",
        owed_minor: "0",
        reimbursements_minor: "0",
        loans_minor: "0",
      },
    });
    const result = (data) => ({ ok: true, json: async () => data });
    globalThis.fetch = async (url, init = {}) => {
      const parsed = new URL(url, "http://localhost");
      if (parsed.pathname === "/auth/session")
        return result({ mode: "local", csrf: "synthetic-csrf" });
      if (parsed.pathname === "/api/v1/books")
        return result({ books: [current], currencies });
      if (parsed.pathname === "/api/v1/state") {
        assert.equal(parsed.searchParams.get("book_id"), book.id);
        return result(state());
      }
      if (parsed.pathname === "/api/v1/access/tokens")
        return result({ schema_version: 1, tokens: [] });
      if (parsed.pathname === "/api/v1/legal-identity-catalog")
        return result({
          catalog: {
            version: "synthetic-1",
            countries: [],
            jurisdictions: [],
            forms: [],
            overlays: [],
            tax_classifications: [],
          },
        });
      if (parsed.pathname === "/api/v1/book-identity")
        return result({
          subject: {
            id: "synthetic-subject",
            revision: 0,
            kind: null,
            identity: {},
          },
          history: [],
          has_more: false,
        });
      assert.equal(parsed.pathname, "/api/v1/settings");
      assert.equal(init.method, "POST");
      const body = JSON.parse(init.body);
      changes.push(body);
      current = { ...current, name: body.name, founder: body.founder, metadata_revision: body.revision + 1 };
      return result(current);
    };
    try {
      await render(
        React.createElement(
          AppTheme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            initial: {
              result: {
                catalog: { books: [book], currencies },
                state: state(),
              },
            },
          }),
        ),
      );
      const shell = document.querySelector(".vult-app-shell");
      const page = document.querySelector(".book-settings-page");
      const form = section("Name").querySelector("form");
      assert.ok(page);
      assert.equal(button("Edit details"), undefined);
      form.elements.namedItem("name").value = "Inline saved synthetic Book";
      await React.act(async () =>
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        ),
      );
      assert.equal(changes.length, 1);
      assert.equal(changes[0].book_id, book.id);
      assert.ok(document.querySelector(".vult-app-shell") === shell);
      assert.ok(document.querySelector(".book-settings-page") === page);
      assert.equal(window.location.pathname, "/settings");
      assert.match(
        document
          .querySelector('button[aria-label^="Switch Book, current Book"]')
          .getAttribute("aria-label"),
        /Inline saved synthetic Book/,
      );
      assert.equal(document.querySelector('[role="dialog"]'), null);
      await click(document.querySelector('button[aria-label="Settings"]'));
      assert.equal(window.location.pathname, "/settings");
      assert.equal(document.querySelector('[role="dialog"]'), null);
    } finally {
      globalThis.fetch = oldFetch;
    }
  });
});
