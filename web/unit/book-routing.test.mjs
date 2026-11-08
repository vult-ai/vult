import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const books = [
  {
    id: "11111111-1111-4111-a111-111111111111",
    name: "First",
    start_date: "2024-01-01",
    currency: "USD",
    scale: 2,
    address: {
      book_id: "11111111-1111-4111-a111-111111111111",
      code: "K7FM",
      slug: "northwind-studio",
      revision: 2,
      aliases: ["k7fm", "old-name", "northwind-studio"],
    },
  },
  {
    id: "22222222-2222-4222-a222-222222222222",
    name: "Second",
    start_date: "2024-01-01",
    currency: "USD",
    scale: 2,
    address: {
      book_id: "22222222-2222-4222-a222-222222222222",
      code: "P4NX",
      slug: "",
      revision: 0,
      aliases: ["p4nx"],
    },
  },
];
test("Book paths resolve codes/aliases only against the authorized catalog", async () =>
  withDOM(async ({ window }) => {
    const r = await loadComponent("src/lib/book-routing.ts");
    assert.equal(r.bookPath(books[0]), "/northwind-studio");
    assert.equal(r.bookPath(books[1], "transactions"), "/P4NX/activity");
    for (const locator of ["K7FM", "k7fm", "old-name", "northwind-studio"])
      assert.equal(
        r.resolveBookRoute(books, `/${locator}/accounts`).id,
        books[0].id,
      );
    assert.throws(() => r.resolveBookRoute([books[1]], "/northwind-studio"));
    assert.equal(r.readBookRoute("/activity").locator, "");
    for (const invalid of [
      "/northwind-studio/arbitrary",
      "/northwind-studio/accounts/extra",
      "/api",
      "/@name",
      "/%2fprivate",
    ])
      assert.equal(r.readBookRoute(invalid).invalid, true);
  }));
test("malformed or conflicting address catalogs are rejected atomically", async () =>
  withDOM(async () => {
    const r = await loadComponent("src/lib/book-routing.ts");
    r.registerBookAddresses(books.map((b) => b.address));
    for (const broken of [
      [{ ...books[0].address, code: "123456" }],
      [{ ...books[0].address, code: "AUTH", aliases: ["auth"] }],
      [{ ...books[0].address, aliases: [7] }],
      [
        books[0].address,
        { ...books[1].address, code: "K7FM", aliases: ["k7fm"] },
      ],
    ])
      assert.throws(() => r.registerBookAddresses(broken));
    assert.equal(r.resolveBookRoute(books, "/K7FM").id, books[0].id);
    r.setBookNavigationLocked(true);
    assert.equal(r.bookNavigationLocked(), true);
    r.setBookNavigationLocked(false);
  }));
test("an address save retains its draft on failure and cannot redirect a different Book", async () =>
  withDOM(async ({ window, render }) => {
    const original = globalThis.fetch;
    const writes = [];
    let finish;
    globalThis.fetch = async (url, options) => {
      if (String(url).endsWith("/session"))
        return { ok: true, json: async () => ({ csrf: "synthetic" }) };
      assert.equal(options?.method, "POST");
      writes.push(JSON.parse(options.body));
      return new Promise((resolve) => {
        finish = resolve;
      });
    };
    try {
      const { BookAddressSettings } = await loadComponent(
        "src/components/book/book-address-settings.tsx",
      );
      const { act } = await import("react");
      const change = async (value) =>
        act(async () => {
          const input = window.document.querySelector(
            'input[name="book-slug"]',
          );
          Object.getOwnPropertyDescriptor(
            window.HTMLInputElement.prototype,
            "value",
          ).set.call(input, value);
          input.dispatchEvent(new window.Event("input", { bubbles: true }));
        });
      const submit = async () =>
        act(async () =>
          window.document
            .querySelector("form")
            .dispatchEvent(
              new window.Event("submit", { bubbles: true, cancelable: true }),
            ),
        );
      window.history.replaceState(null, "", "/northwind-studio/settings");
      await render(
        React.createElement(BookAddressSettings, { book: books[0] }),
      );
      await change("next-address");
      await submit();
      assert.deepEqual(writes[0], {
        book_id: books[0].id,
        slug: "next-address",
        revision: 2,
      });
      await act(async () =>
        finish({
          ok: false,
          status: 422,
          json: async () => ({ error: "choose another address" }),
        }),
      );
      assert.equal(
        window.document.querySelector('input[name="book-slug"]').value,
        "next-address",
      );
      await submit();
      await render(
        React.createElement(BookAddressSettings, { book: books[1] }),
      );
      window.history.replaceState(null, "", "/P4NX/settings");
      await act(async () =>
        finish({
          ok: true,
          json: async () => ({
            ...books[0].address,
            slug: "next-address",
            revision: 3,
            aliases: [...books[0].address.aliases, "next-address"],
          }),
        }),
      );
      assert.equal(window.location.pathname, "/P4NX/settings");
      assert.equal(
        window.document.querySelector('input[name="book-slug"]').value,
        "",
      );
    } finally {
      globalThis.fetch = original;
    }
  }));

test("Book ID copy stays separate from the editable Book URL and never submits a save", async () =>
  withDOM(async ({ window, render }) => {
    const previous = globalThis.fetch;
    const copied = [];
    globalThis.fetch = () => {
      throw Error("Copying a Book ID must not request or save Book data.");
    };
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: { writeText: async (value) => copied.push(value) },
    });
    try {
      const { BookAddressSettings } = await loadComponent(
        "src/components/book/book-address-settings.tsx",
      );
      const { act } = await import("react");
      await render(
        React.createElement(BookAddressSettings, { book: books[0] }),
      );
      assert.equal(document.querySelector("h2").textContent, "Address");
      assert.equal(
        document.querySelector(".book-url-id-value").textContent.trim(),
        "K7FM",
      );
      assert.equal(
        document.querySelector(".book-url-host").textContent.trim(),
        "localhost:3100/",
      );
      assert.equal(document.querySelectorAll("input").length, 1);
      const slug = document.querySelector('input[aria-label="Book URL"]');
      assert.equal(slug.name, "book-slug");
      assert.equal(slug.value, "northwind-studio");
      await act(async () =>
        document.querySelector('button[aria-label="Copy Book ID"]').click(),
      );
      assert.deepEqual(copied, ["K7FM"]);
      assert.equal(slug.value, "northwind-studio");
      await render(
        React.createElement(BookAddressSettings, { book: books[1] }),
      );
      assert.equal(
        document.querySelector(".book-url-id-value").textContent.trim(),
        "P4NX",
      );
      assert.equal(document.querySelector('input[name="book-slug"]').value, "");
      Object.defineProperty(window.navigator, "clipboard", {
        configurable: true,
        value: undefined,
      });
      await act(async () =>
        document.querySelector('button[aria-label="Copy Book ID"]').click(),
      );
      assert.equal(window.getSelection().toString(), "P4NX");
      assert.match(document.body.textContent, /Book ID is selected/);
      assert.deepEqual(copied, ["K7FM"]);
    } finally {
      globalThis.fetch = previous;
    }
  }));

test("rolling, fixed, personal entrypoints and user memory have distinct semantics", async () =>
  withDOM(async ({ window }) => {
    const p = await loadComponent("src/lib/dashboard-location.ts");
    window.history.replaceState({ retained: 1 }, "", "/northwind-studio?period=30d");
    const first = p.readDashboardLocation("2024-01-01", "2026-10-27", books[0]);
    const later = p.readDashboardLocation("2024-01-01", "2026-10-28", books[0]);
    assert.notDeepEqual(first.range, later.range);
    const copied = p.currentViewURL(books[0], first.range);
    const u = new URL(copied);
    assert.equal(u.pathname, "/northwind-studio");
    assert.equal(u.searchParams.get("from"), first.range.from);
    assert.equal(u.searchParams.get("to"), first.range.to);
    assert.equal(u.searchParams.has("period"), false);
    assert.equal(u.searchParams.has("book"), false);
    window.history.replaceState(null, "", u);
    assert.deepEqual(
      p.readDashboardLocation("2024-01-01", "2026-11-30", books[0]).range,
      first.range,
    );
    p.setDashboardPreferenceScope("one");
    p.rememberDashboardSelection(books[0], first);
    window.history.replaceState(null, "", "/northwind-studio");
    assert.equal(
      p.readDashboardLocation("2024-01-01", "2026-10-28", books[0]).period,
      "last-30-days",
    );
    p.setDashboardPreferenceScope("two");
    assert.equal(
      p.readDashboardLocation("2024-01-01", "2026-10-28", books[0]).period,
      "last-30-days",
      "a Book with no remembered period opens on the last 30 days",
    );
    window.history.replaceState(null, "", "/northwind-studio?period=month");
    assert.deepEqual(
      p.readDashboardLocation("2024-01-01", "2026-10-01", books[0]).range,
      { from: "2026-10-01", to: "2026-10-31" },
    );
  }));

test("legacy dates survive canonical replacement; ambiguous new links never guess", async () =>
  withDOM(async ({ window }) => {
    const p = await loadComponent("src/lib/dashboard-location.ts");
    window.history.replaceState(
      { preview: "kept" },
      "",
      "/dashboard?book=first.11111111&range=2025-08-04..2025-08-20&preset=custom&entry=original",
    );
    assert.equal(p.resolveDashboardLocationBook(books).id, books[0].id);
    const s = p.readDashboardLocation("2024-01-01", "2026-10-27", books[0]);
    p.writeDashboardLocation(books[0], s, "replace");
    assert.equal(window.location.pathname, "/northwind-studio");
    assert.equal(
      window.location.search,
      "?entry=original&from=2025-08-04&to=2025-08-20",
    );
    assert.deepEqual(window.history.state, { preview: "kept" });
    for (const q of [
      "?from=2026-10-01&to=2026-10-31&period=month",
      "?period=month&period=year",
      "?from=2026-02-30&to=2026-03-01",
      "?period=unknown",
      "?from=2026-10-01",
    ]) {
      window.history.replaceState(null, "", `/northwind-studio${q}`);
      assert.throws(
        () => p.readDashboardLocation("2024-01-01", "2026-10-27", books[0]),
        q,
      );
    }
    window.history.replaceState(null, "", `/northwind-studio?book_id=${books[1].id}`);
    assert.throws(() => p.resolveDashboardLocationBook(books));
  }));

test("copy uses confirmed exact dates without waiting on clipboard permission", async () =>
  withDOM(async ({ window, render }) => {
    let release;
    const writes = [];
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: (text) => {
          writes.push(text);
          return new Promise((r) => {
            release = r;
          });
        },
      },
    });
    const { CopyCurrentView } = await loadComponent(
      "src/components/book/copy-current-view.tsx",
    );
    const { act } = await import("react");
    window.history.replaceState(null, "", "/northwind-studio?period=month");
    await render(
      React.createElement(CopyCurrentView, {
        book: books[0],
        range: { from: "2026-10-01", to: "2026-10-31" },
      }),
    );
    const button = window.document.querySelector("button");
    await act(async () => button.click());
    assert.match(writes[0], /range=2026-10$/, "a whole month links as its short token");
    assert.notEqual(button.getAttribute("aria-busy"), "true");
    await act(async () => release());
    assert.match(button.textContent, /Copied/);
  }));

test("an old clipboard response cannot confirm a different reporting window", async () =>
  withDOM(async ({ window, render }) => {
    let release;
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: () =>
          new Promise((r) => {
            release = r;
          }),
      },
    });
    const { CopyCurrentView } = await loadComponent(
      "src/components/book/copy-current-view.tsx",
    );
    const { act } = await import("react");
    await render(
      React.createElement(CopyCurrentView, {
        book: books[0],
        range: { from: "2026-10-01", to: "2026-10-31" },
      }),
    );
    await act(async () => window.document.querySelector("button").click());
    await render(
      React.createElement(CopyCurrentView, {
        book: books[0],
        range: { from: "2026-09-01", to: "2026-09-30" },
      }),
    );
    await act(async () => release());
    assert.equal(
      window.document.querySelector("button").getAttribute("aria-label"),
      "Copy current view",
    );
  }));
