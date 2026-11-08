import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("canonical presets retain full calendar periods and rolling days that end today", async () => {
  const { dashboardRange, dashboardPeriods } = await loadComponent(
    "src/lib/dashboard-period.ts",
  );
  const expected = {
    today: ["2026-10-27", "2026-10-27"],
    yesterday: ["2026-10-26", "2026-10-26"],
    "last-7-days": ["2026-10-21", "2026-10-27"],
    "last-30-days": ["2026-09-28", "2026-10-27"],
    "last-90-days": ["2026-07-30", "2026-10-27"],
    week: ["2026-10-26", "2026-11-01"],
    "last-week": ["2026-10-19", "2026-10-25"],
    month: ["2026-10-01", "2026-10-31"],
    "last-month": ["2026-09-01", "2026-09-30"],
    quarter: ["2026-10-01", "2026-12-31"],
    "last-quarter": ["2026-07-01", "2026-09-30"],
    year: ["2026-01-01", "2026-12-31"],
    "last-year": ["2025-01-01", "2025-12-31"],
    "year-to-date": ["2026-01-01", "2026-10-27"],
    "last-12-months": ["2025-10-01", "2026-09-30"],
    all: ["2024-01-01", "2026-10-27"],
  };
  assert.equal(dashboardPeriods.length, Object.keys(expected).length);
  for (const { value } of dashboardPeriods) {
    const [from, to] = expected[value];
    assert.deepEqual(
      dashboardRange(value, "2024-01-01", "2026-10-27"),
      { from, to },
      value,
    );
  }
  for (const [period, anchor, from, to] of [
    ["last-7-days", "2024-03-01", "2024-02-24", "2024-03-01"],
    ["week", "2027-01-01", "2026-12-28", "2027-01-03"],
    ["last-quarter", "2026-01-01", "2025-10-01", "2025-12-31"],
    ["month", "2024-02-01", "2024-02-01", "2024-02-29"],
    ["last-12-months", "2026-01-01", "2025-01-01", "2025-12-31"],
    ["last-7-days", "2026-03-30", "2026-03-24", "2026-03-30"],
  ])
    assert.deepEqual(dashboardRange(period, "2020-01-01", anchor), {
      from,
      to,
    });
});

test("Book memory and explicit shared links preserve identity, exact dates and isolation", async () => {
  await withDOM(async ({ window }) => {
    const {
      readDashboardLocation,
      writeDashboardLocation,
      rememberedDashboardSelection,
      currentViewURL,
      setDashboardPreferenceScope,
    } = await loadComponent("src/lib/dashboard-location.ts");
    const a = { id: "book-a", name: "A" },
      b = { id: "book-b", name: "B" };
    const range = { from: "2026-01-01", to: "2026-01-01" };
    window.history.replaceState(null, "", "/dashboard");
    writeDashboardLocation(a, { period: "year-to-date", range }, "push");
    const rolling = window.location.href;
    const shared = currentViewURL(a, range);
    assert.equal(new URL(rolling).searchParams.get("period"), "ytd");
    assert.equal(
      readDashboardLocation("2024-01-01", "2026-01-01", a).period,
      "year-to-date",
    );
    writeDashboardLocation(
      b,
      { period: "custom", range: { from: "2025-02-12", to: "2025-03-15" } },
      "replace",
    );
    assert.deepEqual(
      readDashboardLocation("2024-01-01", "2026-01-01", a),
      { period: "year-to-date", range },
      "another Book's URL must not replace A's preference",
    );
    window.history.replaceState(null, "", "/dashboard");
    assert.equal(
      readDashboardLocation("2024-01-01", "2026-01-01", b).period,
      "custom",
    );
    window.history.replaceState(null, "", shared);
    assert.deepEqual(
      readDashboardLocation("2024-01-01", "2027-07-15", a),
      { period: "custom", range },
      "an old shared link keeps its exact dates and an honest label",
    );
    writeDashboardLocation(a, { period: "custom", range }, "replace");
    assert.equal(
      readDashboardLocation("2024-01-01", "2026-01-01", a).period,
      "custom",
      "custom dates matching a preset remain custom",
    );
    setDashboardPreferenceScope("corrupt-storage-check");
    localStorage.setItem(
      "vult:dashboard-selection:v2:corrupt-storage-check:book-a",
      JSON.stringify({ bookId: b.id, period: "today", range }),
    );
    assert.equal(rememberedDashboardSelection(a.id), undefined);
    localStorage.setItem(
      "vult:dashboard-selection:v2:corrupt-storage-check:book-a",
      '{"bookId":"book-a","period":"today","range":{"from":"2026-02-30","to":"2026-03-01"}}',
    );
    assert.equal(rememberedDashboardSelection(a.id), undefined);
  });
});

test("startup requests the remembered Book's range without contaminating activity filters", async () => {
  await withDOM(async ({ window }) => {
    const { writeDashboardLocation } = await loadComponent(
      "src/lib/dashboard-location.ts",
    );
    const { readInitialBooks } = await loadComponent("src/lib/book-state.ts");
    const { emptyDashboardState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
    const book = {
      id: "book-a",
      name: "A",
      currency: "USD",
      scale: 2,
      start_date: "2020-01-01",
      accounting_timezone: "Pacific/Honolulu",
    };
    window.history.replaceState(null, "", "/dashboard");
    writeDashboardLocation(
      book,
      { period: "custom", range: { from: "2024-02-01", to: "2024-02-29" } },
      "replace",
    );
    window.history.replaceState(null, "", "/dashboard");
    localStorage.setItem("vult:active-book:v1", book.id);
    const original = globalThis.fetch,
      calls = [];
    globalThis.fetch = async (url) => {
      calls.push(String(url));
      const query = new URL(url, window.location.origin).searchParams;
      return {
        ok: true,
        json: async () =>
          String(url).endsWith("/books") ? { books: [book] } :
            query.get("view") === "dashboard" ? emptyDashboardState(book, query) : { book },
      };
    };
    try {
      await readInitialBooks();
      assert.equal(calls.length, 2);
      const q = new URL(calls[1], window.location.origin).searchParams;
      assert.equal(q.get("dashboard_from"), "2024-02-01");
      assert.equal(q.get("dashboard_to"), "2024-02-29");
      assert.equal(q.get("from"), null, "position reads carry no transaction filters");
      assert.equal(q.get("to"), null);
      window.history.replaceState(null, "", "/activity?from=2025-04-01&to=2025-04-30");
      calls.length = 0;
      await readInitialBooks();
      const activity = new URL(calls[1], window.location.origin).searchParams;
      assert.equal(activity.get("dashboard_from"), "2024-02-01", "Activity URL filters cannot replace the remembered dashboard window");
      assert.equal(activity.get("dashboard_to"), "2024-02-29");
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("blocked preference storage cannot break a confirmed report or exact shared URL", async () => {
  await withDOM(async ({ window }) => {
    const { readDashboardLocation, writeDashboardLocation } =
      await loadComponent("src/lib/dashboard-location.ts");
    const book = { id: "book-a", name: "A" };
    const selection = {
      period: "custom",
      range: { from: "2024-02-01", to: "2024-02-29" },
    };
    const descriptor = Object.getOwnPropertyDescriptor(
      globalThis,
      "localStorage",
    );
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: {
        getItem() {
          throw new Error("Storage blocked");
        },
        setItem() {
          throw new Error("Storage blocked");
        },
      },
    });
    try {
      window.history.replaceState(null, "", "/dashboard");
      assert.doesNotThrow(() =>
        writeDashboardLocation(book, selection, "replace"),
      );
      assert.deepEqual(
        readDashboardLocation("2020-01-01", "2026-10-27", book),
        selection,
      );
      window.history.replaceState(null, "", "/dashboard");
      assert.deepEqual(
        readDashboardLocation("2020-01-01", "2026-10-27", book).range,
        selection.range,
        "the current tab retains its confirmed preference when storage is blocked",
      );
    } finally {
      Object.defineProperty(globalThis, "localStorage", descriptor);
    }
  });
});
