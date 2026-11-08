import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "aaaaaaaa-1111-4111-8111-111111111111",
  name: "Synthetic",
  address: {
    book_id: "aaaaaaaa-1111-4111-8111-111111111111",
    code: "K7FM",
    slug: "synthetic",
    aliases: ["k7fm", "synthetic"],
    revision: 0,
  },
};

test("Accounts keeps its Book, hierarchy filter and exact reporting dates through date selection", async () => {
  await withDOM(async ({ window }) => {
    const { readDashboardLocation, writeDashboardLocation } =
      await loadComponent("src/lib/dashboard-location.ts");
    window.history.replaceState(
      { inspector: "retained" },
      "",
      "/synthetic/accounts?from=2026-01-01&to=2026-09-30&group=asset",
    );
    assert.deepEqual(
      readDashboardLocation("2020-01-01", "2026-10-28", book).range,
      { from: "2026-01-01", to: "2026-09-30" },
    );
    const next = {
      period: "custom",
      range: { from: "2025-07-01", to: "2026-06-30" },
    };
    writeDashboardLocation(book, next, "push");
    assert.match(window.location.pathname, /\/accounts$/);
    assert.equal(new URLSearchParams(window.location.search).get("group"), "asset");
    assert.deepEqual(
      readDashboardLocation("2020-01-01", "2026-10-28", book),
      next,
    );
    assert.deepEqual(window.history.state, { inspector: "retained" });
  });
});

test("an Accounts link for another Book cannot replace this Book's remembered reporting context", async () => {
  await withDOM(async ({ window }) => {
    const { readDashboardLocation, rememberDashboardSelection } =
      await loadComponent("src/lib/dashboard-location.ts");
    const own = {
      period: "custom",
      range: { from: "2026-02-01", to: "2026-03-31" },
    };
    rememberDashboardSelection(book, own);
    window.history.replaceState(
      null,
      "",
      "/other/accounts?from=2025-01-01&to=2025-12-31",
    );
    assert.deepEqual(
      readDashboardLocation("2020-01-01", "2026-10-28", book),
      own,
    );
  });
});
