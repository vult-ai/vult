import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

const model = await loadComponent("src/lib/user-preferences.ts");
const dates = await loadComponent("src/lib/date-format.ts");
const { money, parseMoney } = await loadComponent("src/lib/api.ts");
const {
  createPreferenceStore,
  preferenceStorageKey,
  parseUserPreferences,
  DEFAULT_USER_PREFERENCES: defaults,
} = model;
const memory = () => {
  const data = new Map();
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => data.set(key, value),
  };
};

test("User and browser preferences are isolated without using Book identity", () => {
  const storage = memory();
  const first = createPreferenceStore(
    { kind: "user", id: "synthetic-user-one" },
    storage,
  );
  const second = createPreferenceStore(
    { kind: "user", id: "synthetic-user-two" },
    storage,
  );
  const browser = createPreferenceStore({ kind: "browser" }, storage);
  first.update({
    regional_format: "de-DE",
    date_format: "day-month-year",
    negative_style: "parentheses",
  });
  second.update({ regional_format: "en-IN", time_format: "12h" });
  assert.equal(browser.getSnapshot().value, defaults);
  assert.equal(second.getSnapshot().value.date_format, "standard");
  assert.equal(
    createPreferenceStore(
      { kind: "user", id: "synthetic-user-one" },
      storage,
    ).getSnapshot().value.regional_format,
    "de-DE",
  );
  assert.equal(
    createPreferenceStore(
      { kind: "user", id: "synthetic-user-two" },
      storage,
    ).getSnapshot().value.regional_format,
    "en-IN",
  );
  assert.equal(storage.data.size, 2);
  assert.equal(JSON.stringify([...storage.data]).includes("book_id"), false);
  assert.notEqual(
    preferenceStorageKey({ kind: "user", id: "browser" }),
    preferenceStorageKey({ kind: "browser" }),
  );
  assert.notEqual(
    preferenceStorageKey({ kind: "user", id: "a:b" }),
    preferenceStorageKey({ kind: "user", id: "a%3Ab" }),
  );
  assert.throws(
    () => preferenceStorageKey({ kind: "user", id: "" }),
    /User identity/,
  );
  assert.ok(Object.isFrozen(first.getSnapshot().value));
});

test("unsupported language, malformed values and accounting fields cannot become preferences", () => {
  for (const patch of [
    { language: "pl" },
    { regional_format: "unknown" },
    { date_format: ["iso"] },
    { time_format: ["12h"] },
    { negative_style: ["minus"] },
    { digit_grouping: 1 },
    { week_start: "Sunday" },
    { accounting_timezone: "Europe/Warsaw" },
    { currency: "EUR" },
    { book_id: "synthetic-book" },
    { version: 2 },
  ]) {
    const storage = memory();
    const store = createPreferenceStore({ kind: "browser" }, storage);
    assert.throws(() => store.update(patch), /Unsupported presentation/);
    assert.equal(store.getSnapshot().value, defaults);
    assert.equal(storage.data.size, 0);
  }
  assert.equal(parseUserPreferences({ ...defaults, language: "de" }), null);
  assert.equal(parseUserPreferences({ ...defaults, extra: true }), null);
});

test("newer, corrupt and foreign-owner storage is retained rather than overwritten", () => {
  const scope = { kind: "user", id: "synthetic-one" };
  const key = preferenceStorageKey(scope);
  for (const raw of [
    "{broken",
    JSON.stringify({ owner: scope, preferences: { ...defaults, version: 2 } }),
    JSON.stringify({
      owner: { kind: "user", id: "synthetic-other" },
      preferences: { ...defaults, regional_format: "de-DE" },
    }),
  ]) {
    const storage = memory();
    storage.setItem(key, raw);
    const store = createPreferenceStore(scope, storage);
    assert.equal(store.getSnapshot().persistence, "invalid");
    assert.equal(store.getSnapshot().value.regional_format, "en-US");
    store.update({ date_format: "iso" });
    assert.equal(store.getSnapshot().value.date_format, "iso");
    assert.equal(storage.getItem(key), raw);
    assert.equal(store.getSnapshot().persistence, "invalid");
  }
});

test("a late newer envelope is retained and compatible disjoint choices merge before saving", () => {
  const storage = memory();
  const scope = { kind: "browser" };
  const first = createPreferenceStore(scope, storage);
  const second = createPreferenceStore(scope, storage);
  first.update({ regional_format: "de-DE" });
  second.update({ date_format: "iso" });
  assert.equal(second.getSnapshot().value.regional_format, "de-DE");
  const future = JSON.stringify({
    owner: scope,
    preferences: { ...defaults, version: 2 },
  });
  storage.setItem(first.key, future);
  first.update({ time_format: "12h" });
  assert.equal(first.getSnapshot().persistence, "invalid");
  assert.equal(storage.getItem(first.key), future);
});

test("denied reads and failed writes apply only local presentation with truthful status", () => {
  let writes = 0;
  const denied = createPreferenceStore(
    { kind: "browser" },
    {
      getItem() {
        throw Error("denied");
      },
      setItem() {
        writes++;
      },
    },
  );
  denied.update({ time_format: "12h" });
  assert.equal(writes, 0);
  assert.equal(denied.getSnapshot().persistence, "unavailable");
  const full = createPreferenceStore(
    { kind: "browser" },
    {
      getItem: () => null,
      setItem() {
        throw Error("quota");
      },
    },
  );
  full.update({ date_format: "iso" });
  assert.equal(full.getSnapshot().value.date_format, "iso");
  assert.equal(full.getSnapshot().persistence, "unavailable");
  const noStorage = createPreferenceStore({ kind: "browser" }, null);
  noStorage.update({ digit_grouping: false });
  assert.equal(noStorage.getSnapshot().value.digit_grouping, false);
  assert.equal(noStorage.getSnapshot().persistence, "unavailable");
});

test("storage reloads notify consumers without changing another owner's snapshot", () => {
  const storage = memory();
  const scope = { kind: "user", id: "synthetic-one" };
  const store = createPreferenceStore(scope, storage);
  const other = createPreferenceStore(
    { kind: "user", id: "synthetic-two" },
    storage,
  );
  let notifications = 0;
  const unsubscribe = store.subscribe(() => notifications++);
  storage.setItem(
    store.key,
    JSON.stringify({
      owner: scope,
      preferences: { ...defaults, date_format: "iso" },
    }),
  );
  store.reload();
  assert.equal(store.getSnapshot().value.date_format, "iso");
  assert.equal(other.getSnapshot().value.date_format, "standard");
  assert.equal(notifications, 1);
  unsubscribe();
  store.update({ time_format: "12h" });
  assert.equal(notifications, 1);
});

test("date-only source facts keep their ISO identity across locale, DST and year boundaries", () => {
  const previous = process.env.TZ;
  const now = new Date("2026-10-27T12:00:00Z");
  const source = Object.freeze({
    economic_date: "2026-03-08",
    posting_date: "2026-03-08",
    source_date: "2025-12-31",
    recorded_at: "2026-03-09T01:30:00.123456Z",
    minor: "9223372036854775807",
    currency: "USD",
    scale: 2,
  });
  const original = JSON.stringify(source);
  try {
    for (const zone of [
      "America/New_York",
      "Europe/Warsaw",
      "Pacific/Kiritimati",
      "Pacific/Honolulu",
    ]) {
      process.env.TZ = zone;
      for (const day of [
        "2026-03-08",
        "2026-11-01",
        "2025-12-31",
        "2026-01-01",
        "2024-02-29",
      ]) {
        const [year, month, date] = day.split("-");
        for (const regional_format of [
          "en-US",
          "en-GB",
          "de-DE",
          "pl-PL",
          "en-IN",
        ]) {
          assert.equal(
            dates.formatDate(day, "compact", now, {
              ...defaults,
              regional_format,
              date_format: "iso",
            }),
            day,
          );
          assert.equal(
            dates.formatDate(day, "full", now, {
              ...defaults,
              regional_format,
              date_format: "day-month-year",
            }),
            `${date}/${month}/${year}`,
          );
          assert.equal(
            dates.formatDate(day, "full", now, {
              ...defaults,
              regional_format,
              date_format: "month-day-year",
            }),
            `${month}/${date}/${year}`,
          );
          assert.equal(dates.dateISO(day), day);
        }
      }
    }
  } finally {
    if (previous === undefined) delete process.env.TZ;
    else process.env.TZ = previous;
  }
  assert.equal(JSON.stringify(source), original);
  assert.equal(dates.uploadedDate("10/27/2026"), "2026-10-27");
  assert.equal(
    dates.formatDate("2025-02-29", "full", now, {
      ...defaults,
      date_format: "iso",
    }),
    "2025-02-29",
  );
  assert.equal(
    dates.formatDateRange("2026-03-08", "2026-03-09", "full", now, {
      ...defaults,
      date_format: "iso",
    }),
    "2026-03-08 – 2026-03-09",
  );
  assert.equal(
    dates.formatDate("2026-10-27", "full", now, {
      ...defaults,
      regional_format: "pl-PL",
    }),
    // The interface is in English: English month names in the region's order.
    "27 October 2026",
  );
  assert.equal(
    dates.formatDate("2025-10-27", "compact", now, {
      ...defaults,
      regional_format: "de-DE",
    }),
    "27 Oct 2025",
  );
});

test("explicit clocks preserve original subsecond precision and the UTC audit basis", () => {
  const now = new Date("2026-10-27T12:00:00Z");
  const prefs = { ...defaults, date_format: "iso", time_format: "12h" };
  assert.equal(
    dates.formatDateTime(
      "2026-11-01T01:30:00.123456789-04:00",
      "full",
      now,
      prefs,
    ),
    "2026-11-01 · 5:30:00.123456789 AM UTC",
  );
  assert.equal(
    dates.formatDateTime(
      "2026-11-01T01:30:00.123456789-05:00",
      "full",
      now,
      prefs,
    ),
    "2026-11-01 · 6:30:00.123456789 AM UTC",
  );
  assert.equal(
    dates.formatDateTime("2026-10-27T00:00:00Z", "full", now, prefs),
    "2026-10-27 · 12:00:00 AM UTC",
  );
  assert.equal(
    dates.formatDateTime("2026-10-27T12:00:00Z", "full", now, prefs),
    "2026-10-27 · 12:00:00 PM UTC",
  );
  assert.equal(
    dates.formatDateTime("2026-10-27T20:30:00Z", "full", now, prefs),
    "2026-10-27 · 8:30:00 PM UTC",
  );
  assert.equal(
    dates.formatDateTime("unresolved source time", "full", now, prefs),
    "unresolved source time",
  );
});

test("regional money preserves exact 64-bit units, currency scale and existing input parsing", () => {
  const usd = { currency: "USD", scale: 2 };
  const raw = "9223372036854775807";
  for (const regional_format of ["en-US", "en-GB", "de-DE", "pl-PL", "en-IN"]) {
    for (const digit_grouping of [true, false]) {
      const preferences = {
        ...defaults,
        regional_format,
        digit_grouping,
        negative_style: "parentheses",
      };
      const output = money(raw, usd, preferences);
      assert.equal(output.replace(/\D/g, ""), raw);
      assert.equal(money(`-${raw}`, usd, preferences), `(${output})`);
      assert.equal(money("0", usd, preferences).includes("("), false);
      assert.equal(
        money("12345", { currency: "JPY", scale: 0 }, preferences).replace(
          /\D/g,
          "",
        ),
        "12345",
      );
      assert.equal(
        money("12345", { currency: "KWD", scale: 3 }, preferences).replace(
          /\D/g,
          "",
        ),
        "12345",
      );
    }
  }
  assert.equal(
    money("123456789", usd, { ...defaults, regional_format: "en-IN" }),
    "$12,34,567.89",
  );
  assert.equal(
    money("123456789", usd, { ...defaults, regional_format: "de-DE" }),
    "1.234.567,89\u00a0$",
  );
  assert.equal(
    money("123456789", usd, {
      ...defaults,
      regional_format: "de-DE",
      digit_grouping: false,
    }),
    "1234567,89\u00a0$",
  );
  assert.equal(parseMoney("1234.56", 2), "123456");
  assert.throws(() => parseMoney("1.234,56", 2), /without separators/);
});
