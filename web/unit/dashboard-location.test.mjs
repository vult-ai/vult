import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("Dashboard rolling and copied links discard inactive Statements settings without changing their range", async () => {
  await withDOM(async ({ window }) => {
    const { readDashboardLocation, writeDashboardLocation, currentViewURL } =
      await loadComponent("src/lib/dashboard-location.ts");
    const book = {
      id: "book-query",
      name: "Northwind Studio",
      address: {
        book_id: "book-query",
        code: "K7FM",
        slug: "northwind-studio",
        aliases: ["northwind-studio"],
        revision: 1,
      },
    };
    const stale =
      "as_of=2026-12-31&compare=explicit&compare_to=2026-11-30&report=balance_sheet&compare_from=2026-11-01&review_report=balance_sheet&columns=months";
    for (const page of ["", "/accounts"])
      for (const mode of ["push", "replace"]) {
        window.history.replaceState(
          { preview: "retained" },
          "",
          `/northwind-studio${page}?${stale}&period=7d&keep=value`,
        );
        const selection = readDashboardLocation(
          "2024-01-01",
          "2026-10-29",
          book,
        );
        assert.equal(selection.period, "last-7-days");
        const range = { ...selection.range };
        const copy = new URL(currentViewURL(book, range));
        assert.equal(copy.pathname, `/northwind-studio${page}`);
        assert.equal(
          copy.search,
          `?keep=value&from=${range.from}&to=${range.to}`,
        );
        assert.equal(
          new URL(window.location.href).searchParams.get("as_of"),
          "2026-12-31",
          "copying never mutates the current location",
        );
        writeDashboardLocation(book, selection, mode);
        assert.equal(window.location.pathname, `/northwind-studio${page}`);
        assert.equal(window.location.search, "?keep=value&period=7d");
        assert.deepEqual(window.history.state, { preview: "retained" });
        assert.deepEqual(
          readDashboardLocation("2024-01-01", "2026-10-29", book).range,
          range,
        );
        assert.deepEqual(
          readDashboardLocation("2024-01-01", "2026-10-30", book).range,
          { from: "2026-10-24", to: "2026-10-30" },
          "the clean rolling link advances naturally on a later day",
        );
        window.history.replaceState(null, "", copy);
        assert.deepEqual(
          readDashboardLocation("2024-01-01", "2026-11-15", book),
          { period: "custom", range },
          "the copied link retains its exact dates",
        );
      }
    window.history.replaceState(null, "", `/northwind-studio?${stale}&period=7d`);
    const selection = readDashboardLocation("2024-01-01", "2026-10-29", book);
    writeDashboardLocation(book, selection, "push");
    assert.equal(
      window.location.search,
      "?period=7d",
      "the owner's reported link reduces to its meaningful preset",
    );
  });
});

test("Statements retain their active as-of and comparison settings during Dashboard URL serialization", async () => {
  await withDOM(async ({ window }) => {
    const { writeDashboardLocation, currentViewURL } = await loadComponent(
      "src/lib/dashboard-location.ts",
    );
    const book = {
      id: "book-query",
      name: "Northwind Studio",
      address: {
        book_id: "book-query",
        code: "K7FM",
        slug: "northwind-studio",
        aliases: ["northwind-studio"],
        revision: 1,
      },
    };
    window.history.replaceState(
      null,
      "",
      "/northwind-studio/statements?as_of=2026-12-31&report=balance_sheet&compare=explicit&compare_to=2026-11-30&columns=months",
    );
    const original = new URL(window.location.href),
      selection = {
        period: "last-7-days",
        range: { from: "2026-10-22", to: "2026-10-28" },
      };
    for (const url of [
      new URL(currentViewURL(book, selection.range)),
      (() => {
        writeDashboardLocation(book, selection, "push");
        return new URL(window.location.href);
      })(),
    ]) {
      assert.equal(url.pathname, "/northwind-studio/statements");
      for (const key of ["as_of", "report", "compare", "compare_to", "columns"])
        assert.equal(
          url.searchParams.get(key),
          original.searchParams.get(key),
          key,
        );
    }
  });
});

test("dashboard links validate actual dates and preserve exact dates as presets age", async () => {
  await withDOM(async ({ window }) => {
    const {
      dashboardLocationRange,
      readDashboardLocation,
      writeDashboardLocation,
    } = await loadComponent("src/lib/dashboard-location.ts");
    for (const search of [
      "?from=2026-02-30&to=2026-03-01",
      "?from=2026-10-02&to=2026-10-01",
      "?from=2026-10-01",
      "?from=x&to=y",
    ])
      assert.equal(dashboardLocationRange(search), undefined);
    assert.deepEqual(dashboardLocationRange("?from=2024-02-29&to=2024-03-01"), {
      from: "2024-02-29",
      to: "2024-03-01",
    });
    window.history.replaceState(
      { preview: "retained" },
      "",
      "/dashboard?from=2026-01-01&to=2026-12-31&period=year&keep=value",
    );
    const same = readDashboardLocation("2024-01-01", "2026-10-26");
    assert.equal(same.period, "year");
    const later = readDashboardLocation("2024-01-01", "2027-01-02");
    assert.equal(later.period, "custom");
    assert.deepEqual(later.range, same.range);
    writeDashboardLocation({ id: "book-b", name: "B" }, same, "replace");
    assert.equal(
      new URLSearchParams(window.location.search).get("keep"),
      "value",
    );
    assert.deepEqual(window.history.state, { preview: "retained" });
  });
});

test("full-month links stay monthly across days while older to-date links keep their exact dates", async () => {
  await withDOM(async ({ window }) => {
    const { readDashboardLocation } = await loadComponent(
      "src/lib/dashboard-location.ts",
    );
    window.history.replaceState(
      null,
      "",
      "/dashboard?range=2026-10-01..2026-10-31",
    );
    assert.equal(
      readDashboardLocation("2020-01-01", "2026-10-01").period,
      "month",
    );
    assert.equal(
      readDashboardLocation("2020-01-01", "2026-10-26").period,
      "month",
    );
    const nextMonth = readDashboardLocation("2020-01-01", "2026-11-01");
    assert.equal(nextMonth.period, "last-month");
    assert.deepEqual(nextMonth.range, { from: "2026-10-01", to: "2026-10-31" });
    window.history.replaceState(
      null,
      "",
      "/dashboard?range=2026-10-01..2026-10-26",
    );
    assert.deepEqual(readDashboardLocation("2020-01-01", "2026-10-26"), {
      period: "custom",
      range: { from: "2026-10-01", to: "2026-10-26" },
    });
  });
});

test("startup reads the linked Book and range before finances, without changing transaction filters", async () => {
  await withDOM(async ({ window }) => {
    const { readInitialBooks } = await loadComponent("src/lib/book-state.ts");
    const { emptyDashboardState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
    const original = globalThis.fetch,
      calls = [];
    const a = { id: "book-a", name: "A", currency: "USD", scale: 2 },
      b = { ...a, id: "book-b", name: "B" };
    window.history.replaceState(
      null,
      "",
      "/dashboard?book_id=book-b&from=2026-02-01&to=2026-03-19",
    );
    localStorage.setItem("vult:active-book:v1", a.id);
    globalThis.fetch = async (url) => {
      calls.push(String(url));
      return {
        ok: true,
        json: async () =>
          String(url).endsWith("/books") ? { books: [a, b] } : emptyDashboardState(b, new URL(url, window.location.origin).searchParams),
      };
    };
    try {
      assert.equal((await readInitialBooks()).state.book.id, b.id);
      const query = new URL(calls.at(-1), window.location.origin).searchParams;
      assert.equal(query.get("book_id"), b.id);
      assert.equal(query.get("dashboard_from"), "2026-02-01");
      assert.equal(query.get("dashboard_to"), "2026-03-19");
      assert.equal(query.get("from"), null, "position reads carry no transaction filters");
      window.history.replaceState(
        null,
        "",
        "/dashboard?book=b.book-b&range=2026-02-01..2026-03-19",
      );
      assert.equal((await readInitialBooks()).state.book.id, b.id);
      window.history.replaceState(null, "", "/dashboard?book_id=missing");
      const before = calls.length;
      await assert.rejects(readInitialBooks(), /Book link.*available/);
      assert.equal(
        calls.length,
        before + 1,
        "unavailable links do not read another book's finances",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("readable Book links retain identity after renaming and reject ambiguous prefixes", async () => {
  await withDOM(async () => {
    const { dashboardBookReference, resolveDashboardLocationBook } =
      await loadComponent("src/lib/dashboard-location.ts");
    const book = {
      id: "e5c628cc-03b8-414c-a64a-44dfbbc34434",
      name: "Northwind Studio",
    };
    const reference = dashboardBookReference(book);
    assert.equal(reference, "northwind-studio.e5c628cc");
    assert.equal(
      resolveDashboardLocationBook(
        [{ ...book, name: "Renamed" }],
        `?book=${reference}`,
      ).id,
      book.id,
    );
    assert.throws(() =>
      resolveDashboardLocationBook(
        [book, { ...book, id: "e5c628cc-collision" }],
        `?book=${reference}`,
      ),
    );
  });
});

test("confirmed dates survive remount, Back/Forward and failed or out-of-order reads", async () => {
  await withDOM(async ({ render, window }) => {
    const { useDashboard } = await loadComponent("src/lib/use-dashboard.ts");
    const { rememberedDashboardSelection } = await loadComponent(
      "src/lib/dashboard-location.ts",
    );
    const book = {
      id: "book-a",
      name: "A",
      start_date: "2024-01-01",
      accounting_timezone: "UTC",
    };
    const month = {
      book_id: book.id,
      version: "1",
      period: { from: "2026-10-01", to: "2026-10-26" },
      cash_minor: "100",
      money_in_minor: "20",
    };
    window.history.replaceState(
      null,
      "",
      "/dashboard?book_id=book-a&from=2026-10-01&to=2026-10-26&period=month",
    );
    let report;
    const requests = [],
      original = globalThis.fetch;
    globalThis.fetch = (url) =>
      new Promise((resolve, reject) => requests.push({ url, resolve, reject }));
    function Probe() {
      report = useDashboard(book, month);
      return React.createElement(
        "output",
        null,
        report.data?.period.from || "unknown",
      );
    }
    const complete = (i) =>
      React.act(async () => {
        const r = requests[i],
          q = new URL(r.url, window.location.origin).searchParams;
        r.resolve({
          ok: true,
          json: async () => ({
            ...month,
            period: { from: q.get("from"), to: q.get("to") },
          }),
        });
      });
    const choose = (range) =>
      React.act(async () => {
        void report.selectRange(range);
      });
    try {
      await render(React.createElement(Probe, { key: "first" }));
      assert.equal(requests.length, 0);
      const range = { from: "2026-01-01", to: "2026-09-30" };
      await choose(range);
      await complete(0);
      const saved = window.location.href;
      assert.match(saved, /from=2026-01-01&to=2026-09-30/);
      await render(React.createElement(Probe, { key: "reload" }));
      assert.equal(
        report.data,
        undefined,
        "a linked range never flashes the monthly snapshot",
      );
      assert.equal(requests.length, 2);
      await complete(1);
      assert.deepEqual(report.data.period, range);
      assert.equal(window.location.href, saved);
      await choose({ from: "2026-02-01", to: "2026-02-28" });
      await choose({ from: "2026-03-01", to: "2026-03-31" });
      await complete(3);
      await complete(2);
      assert.equal(report.data.period.from, "2026-03-01");
      assert.equal(
        new URL(window.location.href).searchParams.get("range"),
        "2026-03",
        "a whole month links as its short token",
      );
      const previous = window.location.href;
      const remembered = rememberedDashboardSelection(book.id);
      assert.deepEqual(
        remembered.range,
        report.data.period,
        "a stale completion cannot overwrite Book memory",
      );
      await choose({ from: "2026-04-01", to: "2026-04-30" });
      assert.deepEqual(
        rememberedDashboardSelection(book.id),
        remembered,
        "pending dates are not remembered",
      );
      await React.act(async () => requests[4].reject(new Error("Unavailable")));
      assert.equal(window.location.href, previous);
      assert.deepEqual(
        rememberedDashboardSelection(book.id),
        remembered,
        "a failed read keeps the previous preference",
      );
      assert.equal(report.data.period.from, "2026-03-01");
      // Native history navigation emits popstate, using the same cached dates.
      await React.act(async () => {
        window.history.back();
        await new Promise((done) => window.setTimeout(done, 25));
      });
      assert.deepEqual(report.data.period, range);
      await React.act(async () => {
        window.history.forward();
        await new Promise((done) => window.setTimeout(done, 25));
      });
      assert.equal(report.data.period.from, "2026-03-01");
      assert.equal(
        requests.length,
        5,
        "history restores confirmed cached snapshots",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("a whole year, quarter or month links as a short token and reads back", async () => {
  const { rangeToken, tokenRange } = await loadComponent("src/lib/period-step.ts");
  for (const [token, from, to] of [
    ["2020", "2020-01-01", "2020-12-31"],
    ["2020-Q3", "2020-07-01", "2020-09-30"],
    ["2024-02", "2024-02-01", "2024-02-29"],
  ]) {
    assert.deepEqual(tokenRange(token), { from, to }, token);
    assert.equal(rangeToken({ from, to }), token);
  }
  assert.deepEqual(tokenRange("2020-q3"), { from: "2020-07-01", to: "2020-09-30" }, "either case reads");
  assert.equal(rangeToken({ from: "2020-01-02", to: "2020-12-31" }), undefined, "other ranges keep exact dates");
  assert.equal(tokenRange("2020-13"), undefined);
  const { dashboardLocationRange } = await loadComponent("src/lib/dashboard-location.ts");
  assert.deepEqual(dashboardLocationRange("?range=2020"), { from: "2020-01-01", to: "2020-12-31" });
  assert.deepEqual(dashboardLocationRange("?from=2020-01-01&to=2020-12-31"), { from: "2020-01-01", to: "2020-12-31" }, "old links still open");
});
