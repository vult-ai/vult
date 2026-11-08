import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const { ActivitySummary, ActivitySummaryStrip, CollectionInspector, readable } = await loadComponent("unit/helpers/activity-summary.tsx");
const book = { id: "summary-book", currency: "USD", scale: 2 };
const filters = { search: "", kind: "", from: "", to: "", account: "" };
const bucket = (key, label = key) => ({ key, label, income_minor: "12000", expenses_minor: "4500", count: 3 });
const empty = { version: 1, count: 0, first_date: "", last_date: "", income_minor: "0", expenses_minor: "0", net_minor: "0", unit: "month", series: [], categories: [], counterparties: [] };
const full = {
  ...empty, count: 6, first_date: "2025-01-04", last_date: "2025-02-27",
  income_minor: "24000", expenses_minor: "9000", net_minor: "15000",
  series: [bucket("2025-01"), bucket("2025-02")],
  categories: [bucket("acct-1", "Supplies")], counterparties: [bucket("harbor", "Harbor Supply Co.")],
};

test("summaries are checked field by field before they are shown", () => {
  assert.equal(readable(empty), true);
  assert.equal(readable(full), true);
  assert.equal(readable({ ...full, unit: "year", series: [bucket("2024"), bucket("2025")] }), true);
  assert.equal(readable({ ...full, first_date: "2024-01-04", last_date: "2024-02-29" }), true, "a leap day is a date");
  const broken = {
    "no version": { ...full, version: 2 },
    "fractional count": { ...full, count: 1.5 },
    "negative count": { ...full, count: -1 },
    "decimal money": { ...full, net_minor: "150.00" },
    "unknown unit": { ...full, unit: "week" },
    "missing date": { ...full, first_date: undefined },
    "bad date": { ...full, last_date: "2025-13-01" },
    "dates out of order": { ...full, first_date: "2025-03-01" },
    "dates on an empty result": { ...empty, first_date: "2025-01-01", last_date: "2025-01-01" },
    "February 31": { ...full, last_date: "2025-02-31" },
    "February 29 in a common year": { ...full, last_date: "2025-02-29" },
    "series not an array": { ...full, series: {} },
    "null bucket": { ...full, series: [null, bucket("2025-02")] },
    "bucket without key": { ...full, categories: [{ ...bucket("x"), key: "" }] },
    "bucket money as number": { ...full, counterparties: [{ ...bucket("x"), income_minor: 12000 }] },
    "bucket count missing": { ...full, categories: [{ ...bucket("x"), count: undefined }] },
    "period key off unit": { ...full, series: [bucket("2025"), bucket("2025-02")] },
  };
  for (const [name, value] of Object.entries(broken)) assert.equal(readable(value), false, name);
  assert.equal(readable(null), false);
  assert.equal(readable("[]"), false);
});

async function serving(responses, run) {
  const original = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url) => {
    calls.push(String(url));
    const body = responses[Math.min(calls.length, responses.length) - 1];
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  };
  try { await run(calls); } finally { globalThis.fetch = original; }
}
const settle = () => React.act(() => new Promise((done) => setTimeout(done, 0)));

test("the panel shows a full summary, an empty one, and retries a malformed one", async () => {
  await withDOM(async ({ render }) => {
    const props = { book, filters, onFilter() {} };
    await serving([full], async () => {
      await render(React.createElement(ActivitySummary, props));
      await settle();
      const text = document.getElementById("root").textContent;
      assert.match(text, /6 transactions/);
      assert.match(text, /Supplies/);
      assert.match(text, /Harbor Supply Co\./);
    });
    await serving([empty], async () => {
      await render(React.createElement(ActivitySummary, { ...props, filters: { ...filters, search: "none" } }));
      await settle();
      assert.match(document.getElementById("root").textContent, /No transactions match these filters/);
    });
    await serving([{ ...full, series: [null] }, full], async (calls) => {
      await render(React.createElement(ActivitySummary, { ...props, filters: { ...filters, kind: "expense" } }));
      await settle();
      assert.match(document.getElementById("root").textContent, /Couldn’t load this summary/);
      const retry = [...document.querySelectorAll("button")].find((b) => /try again|retry/i.test(b.textContent));
      assert.ok(retry, "a retry control");
      await React.act(async () => retry.click());
      await settle();
      assert.equal(calls.length, 2);
      assert.match(document.getElementById("root").textContent, /6 transactions/);
    });
  });
});

test("the compact strip shows a failure instead of disappearing", async () => {
  await withDOM(async ({ render, window }) => {
    window.HTMLElement.prototype.getBoundingClientRect = () => ({ width: 800, height: 600, top: 0, left: 0, right: 800, bottom: 600 });
    await serving([{ ...full, first_date: undefined }], async () => {
      await render(React.createElement(CollectionInspector, { selected: false }, React.createElement(ActivitySummaryStrip, { book, filters })));
      await settle();
      const strip = document.querySelector(".activity-summary-strip");
      assert.ok(strip, "the strip stays on screen");
      assert.ok(strip.querySelector("button"), "with a way to retry");
    });
  });
});

test("amounts too large for a float still draw finite bars", async () => {
  const huge = "9".repeat(400);
  const data = { ...full, series: [{ ...bucket("2025-01"), income_minor: huge }, { ...bucket("2025-02"), expenses_minor: "-" + huge }] };
  assert.equal(readable(data), true, "an exact amount of any size is valid");
  await withDOM(async ({ render }) => {
    await serving([data], async () => {
      await render(React.createElement(ActivitySummary, { book, filters, onFilter() {} }));
      await settle();
      const bars = [...document.querySelectorAll(".activity-summary-column i")];
      assert.ok(bars.length > 0);
      const heights = bars.map((b) => parseFloat(b.style.height));
      for (const h of heights) assert.ok(Number.isFinite(h), "height is finite");
      const tallest = Math.max(...heights);
      assert.ok(tallest > 40, "the large values still use the chart's height");
    });
  });
});

test("retrying the compact summary shows that it is checking, through a second failure and a success", async () => {
  await withDOM(async ({ render, window }) => {
    window.HTMLElement.prototype.getBoundingClientRect = () => ({ width: 800, height: 600, top: 0, left: 0, right: 800, bottom: 600 });
    const original = globalThis.fetch;
    const pending = [];
    globalThis.fetch = () => new Promise((resolve) => pending.push(resolve));
    const answer = (body) => pending.shift()(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
    const strip = () => document.querySelector(".activity-summary-strip");
    const retryButton = () => [...strip().querySelectorAll("button")].find((b) => /try again/i.test(b.textContent));
    try {
      await render(React.createElement(CollectionInspector, { selected: false }, React.createElement(ActivitySummaryStrip, { book, filters })));
      await settle();
      await React.act(async () => answer({ ...full, series: [null] }));
      await settle();
      assert.ok(retryButton(), "the failure offers a retry");
      await React.act(async () => retryButton().click());
      await settle();
      assert.ok(strip(), "the notice stays while the retry is in flight");
      assert.equal(strip().querySelector(".error-notice").dataset.state, "retrying");
      await React.act(async () => answer({ ...full, first_date: "2025-02-31" }));
      await settle();
      assert.equal(strip().querySelector(".error-notice").dataset.state, "failed", "a second failure is shown again");
      // Retries are spaced by a short cooldown so a fast failure can't be hammered.
      await React.act(() => new Promise((done) => setTimeout(done, 1100)));
      await React.act(async () => retryButton().click());
      await settle();
      assert.equal(strip().querySelector(".error-notice").dataset.state, "retrying");
      await React.act(async () => answer(full));
      await settle();
      assert.equal(strip().querySelector(".error-notice"), null);
      assert.match(strip().textContent, /net · .*6 transactions/);
      assert.match(strip().textContent, /\u00a0in.*\u00a0out/);
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("a long history joins whole years into aligned blocks, and a click filters to one", async () => {
  await withDOM(async ({ render, window }) => {
    const years = Array.from({ length: 27 }, (_, i) => String(2000 + i));
    const long = { ...full, count: 54, first_date: "2000-10-02", last_date: "2026-10-20", unit: "year", series: years.map((y) => bucket(y)) };
    const picked = [];
    await serving([long], async () => {
      await render(React.createElement(ActivitySummary, { book, filters, onFilter: (key, value) => picked.push([key, value]) }));
      await settle();
      const groups = [...document.querySelectorAll(".activity-summary-group")];
      assert.equal(groups.length, 14, "27 years as two-year blocks");
      assert.match(groups[7].getAttribute("aria-label"), /^2014–2015: income \$240\.00, expenses \$90\.00/);
      const before = document.querySelector(".vult-headline").textContent;
      await React.act(async () => groups[7].dispatchEvent(new window.MouseEvent("mouseover", { bubbles: true })));
      assert.equal(document.querySelector(".vult-headline").textContent, before, "hover leaves the figures alone");
      await React.act(async () => groups[7].click());
      assert.deepEqual(picked, [["from", "2014-01-01"], ["to", "2015-12-31"]]);
    });
  });
});

test("a click inside an existing date filter stays within it", async () => {
  await withDOM(async ({ render }) => {
    const months = ["2025-01", "2025-02", "2025-03"];
    const short = { ...full, first_date: "2025-01-10", last_date: "2025-03-20", series: months.map((m) => bucket(m)) };
    const picked = [];
    await serving([short], async () => {
      await render(React.createElement(ActivitySummary, { book, filters: { ...filters, from: "2025-01-10", to: "2025-03-20" }, onFilter: (key, value) => picked.push([key, value]) }));
      await settle();
      const groups = [...document.querySelectorAll(".activity-summary-group")];
      assert.equal(groups.length, 3);
      await React.act(async () => groups[0].click());
      assert.deepEqual(picked, [["from", "2025-01-10"], ["to", "2025-01-31"]]);
    });
  });
});
