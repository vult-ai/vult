import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("routine timestamp labels use dates and minutes while audit instants keep their precision", async () => {
  const { formatReadableDateTime, formatDateTime } = await loadComponent(
    "src/lib/date-format.ts",
  );
  const now = new Date(2026, 10, 2);
  const source = "2026-11-02T18:45:07.720656Z";
  assert.equal(
    formatReadableDateTime(source, "compact", now),
    "Nov 2 at 18:45 UTC",
  );
  assert.equal(
    formatReadableDateTime(source, "full", now),
    "November 2, 2026 at 18:45 UTC",
  );
  assert.equal(
    formatReadableDateTime(source, "compact", now, { time_format: "12h" }),
    "Nov 2 at 6:45 PM UTC",
  );
  assert.equal(
    formatReadableDateTime("2025-12-31T23:30:59.999999-02:00", "compact", now),
    "Jan 1 at 01:30 UTC",
  );
  assert.equal(
    formatReadableDateTime("2025-11-02T00:01:59.999Z", "compact", now),
    "Nov 2, 2025 at 00:01 UTC",
  );
  assert.equal(
    formatReadableDateTime("2026-02-30T12:00:00Z"),
    "2026-02-30T12:00:00Z",
  );
  assert.equal(
    formatReadableDateTime("Unknown source time"),
    "Unknown source time",
  );
  assert.equal(formatReadableDateTime(null), "—");
  assert.equal(
    formatDateTime(source, "compact", now),
    "Nov 2 · 18:45:07.720656 UTC",
  );
});

test("one date policy covers current, consecutive historical and future dates, ranges and year rollover", async () => {
  const { formatDate, formatDateRange, formatMonth } = await loadComponent(
    "src/lib/date-format.ts",
  );
  const now = new Date(2026, 9, 27);
  assert.equal(formatDate("2026-09-01", "compact", now), "Sep 1");
  assert.equal(formatDate("2025-09-01", "compact", now), "Sep 1, 2025");
  assert.equal(formatDate("2025-09-02", "compact", now), "Sep 2, 2025");
  assert.equal(formatDate("2027-01-01", "compact", now), "Jan 1, 2027");
  assert.equal(formatDate("2026-09-01", "full", now), "September 1, 2026");
  assert.equal(formatDate("2025-09-01", "full", now), "September 1, 2025");
  assert.equal(
    formatDateRange("2025-12-31", "2026-01-01", "compact", now),
    "Dec 31, 2025 – Jan 1",
  );
  assert.equal(
    formatDateRange("2025-09-01", "2025-09-02", "compact", now),
    "Sep 1, 2025 – Sep 2, 2025",
  );
  assert.equal(
    formatDateRange("2025-09-01", "2025-09-01", "full", now),
    "September 1, 2025",
  );
  assert.equal(
    formatDate("2026-12-31", "compact", new Date(2027, 0, 1)),
    "Dec 31, 2026",
  );
  assert.equal(formatMonth("2026-09", now), "Sep");
  assert.equal(formatMonth("2025-09", now), "Sep 2025");
});

test("date-only values, imported upload metadata and audit timestamp precision survive formatting", async () => {
  const { dateISO, formatDate, uploadedDate, formatDateTime } =
    await loadComponent("src/lib/date-format.ts");
  const now = new Date(2026, 9, 27);
  assert.equal(formatDate("2024-02-29", "full", now), "February 29, 2024");
  assert.equal(formatDate("2025-02-29", "full", now), "2025-02-29");
  assert.equal(dateISO("2025-02-29"), undefined);
  assert.equal(formatDate(null), "—");
  assert.equal(uploadedDate("09/01/2025"), "2025-09-01");
  assert.equal(uploadedDate("1/2/2026"), "2026-01-02");
  assert.equal(uploadedDate("2025-09-01"), "2025-09-01");
  for (const original of ["29/02/2024", "02/29/2025", "09/01/25", "unknown"])
    assert.equal(uploadedDate(original), original);
  assert.equal(uploadedDate(""), "");
  assert.equal(
    formatDateTime("2026-10-25T19:20:18.688587Z", "full", now),
    "October 25, 2026 · 19:20:18.688587 UTC",
  );
  assert.equal(
    formatDateTime("2025-12-31T23:30:00-02:00", "compact", now),
    "Jan 1 · 01:30:00 UTC",
  );
  assert.equal(
    formatDateTime("2025-09-01 12:00:00.123456", "compact", now),
    "Sep 1, 2025 · 12:00:00.123456 UTC",
  );
  assert.equal(formatDateTime("not a timestamp"), "not a timestamp");
});

test("record dates keep complete accessible values and date-only semantics in lists and details", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: new Date(2026, 9, 27) });
  await withDOM(async ({ render }) => {
    const { RecordDate } = await loadComponent(
      "src/components/ui/record-date.tsx",
    );
    await render(React.createElement(RecordDate, { value: "2026-09-01" }));
    const time = document.querySelector("time");
    assert.equal(time.dateTime, "2026-09-01");
    assert.equal(
      time.querySelector('[aria-hidden="true"]').textContent,
      "Sep 1",
    );
    assert.equal(
      time.querySelector(".sr-only").textContent,
      "September 1, 2026",
    );
    await render(
      React.createElement(RecordDate, { value: "2025-09-01", format: "full" }),
    );
    assert.equal(time.textContent, "September 1, 2025");
    assert.equal(
      time.querySelector(".sr-only"),
      null,
      "full dates already expose their complete accessible value",
    );
  });
});
