import { displayPreferences } from "./display-preferences";
import { wordsLocale, type UserPreferences } from "./user-preferences";

export type DateFormat = "compact" | "full";
export type DatePreferences = Pick<
  UserPreferences,
  "regional_format" | "date_format"
>;

const formatters = new Map<string, Intl.DateTimeFormat>();
const formatter = (options: Intl.DateTimeFormatOptions, locale = "en-US") => {
  const key = `${locale}:${JSON.stringify(options)}`;
  let cached = formatters.get(key);
  if (!cached) {
    cached = new Intl.DateTimeFormat(locale, { timeZone: "UTC", ...options });
    formatters.set(key, cached);
  }
  return cached;
};
const shortDate = formatter({ month: "short", day: "numeric" });
const yearDate = formatter({ month: "short", day: "numeric", year: "numeric" });
const fullDate = formatter({ month: "long", day: "numeric", year: "numeric" });
const shortMonth = formatter({ month: "short" });
const yearMonth = formatter({ month: "short", year: "numeric" });

/** Calendar dates are date-only values, never instants shifted to local time. */
function calendarDate(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const date = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(date.getTime()) &&
    date.toISOString().slice(0, 10) === value
    ? date
    : null;
}

export function dateISO(value: string | null | undefined) {
  return value && calendarDate(value) ? value : undefined;
}

/** Only the viewer's current calendar year is implicit; all other years repeat. */
export function formatDate(
  value: string | null | undefined,
  format: DateFormat = "compact",
  now = new Date(),
  preferences?: DatePreferences,
) {
  if (!value) return "—";
  const date = calendarDate(value);
  if (!date) return value;
  preferences ??= displayPreferences();
  if (preferences?.date_format && preferences.date_format !== "standard") {
    const [year, month, day] = value.split("-");
    if (preferences.date_format === "iso") return value;
    return preferences.date_format === "day-month-year"
      ? `${day}/${month}/${year}`
      : `${month}/${day}/${year}`;
  }
  if (preferences && preferences.regional_format !== "en-US") {
    return formatter(
      {
        month: format === "full" ? "long" : "short",
        day: "numeric",
        ...(format === "full" || date.getUTCFullYear() !== now.getFullYear()
          ? { year: "numeric" }
          : {}),
      },
      wordsLocale(preferences.regional_format),
    ).format(date);
  }
  return (
    format === "full"
      ? fullDate
      : date.getUTCFullYear() === now.getFullYear()
        ? shortDate
        : yearDate
  ).format(date);
}

export function formatDateRange(
  from: string,
  to: string,
  format: DateFormat = "compact",
  now = new Date(),
  preferences?: DatePreferences,
) {
  return from === to
    ? formatDate(from, format, now, preferences)
    : `${formatDate(from, format, now, preferences)} – ${formatDate(to, format, now, preferences)}`;
}

/** Month-grain chart labels retain their grain while following the year rule. */
export function formatMonth(
  value: string,
  now = new Date(),
  preferences?: DatePreferences,
) {
  const date = calendarDate(value.length === 7 ? `${value}-01` : value);
  if (!date) return value || "—";
  if (preferences && preferences.regional_format !== "en-US")
    return formatter(
      {
        month: "short",
        ...(date.getUTCFullYear() !== now.getFullYear()
          ? { year: "numeric" }
          : {}),
      },
      wordsLocale(preferences.regional_format),
    ).format(date);
  return (
    date.getUTCFullYear() === now.getFullYear() ? shortMonth : yearMonth
  ).format(date);
}

/** Imported upload metadata uses US M/D/YYYY dates; never guess a locale.
 * Normalize for display only. Unknown/invalid source text remains unchanged.
 */
export function uploadedDate(value: string | null | undefined) {
  if (!value) return "";
  const parts = /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/.exec(value);
  if (!parts) return value;
  const iso = `${parts[3]}-${parts[1].padStart(2, "0")}-${parts[2].padStart(2, "0")}`;
  return dateISO(iso) || value;
}

/** Audit timestamps stay in UTC and retain the original fractional precision. */
export function formatDateTime(
  value: string | null | undefined,
  format: DateFormat = "compact",
  now = new Date(),
  preferences?: DatePreferences & Pick<UserPreferences, "time_format">,
) {
  return formatTimestamp(value, format, now, preferences, false);
}

/** Routine UI labels need dates and minutes; the original instant stays intact. */
export function formatReadableDateTime(
  value: string | null | undefined,
  format: DateFormat = "compact",
  now = new Date(),
  preferences?: DatePreferences & Pick<UserPreferences, "time_format">,
) {
  return formatTimestamp(value, format, now, preferences, true);
}

function formatTimestamp(
  value: string | null | undefined,
  format: DateFormat,
  now: Date,
  preferences:
    (DatePreferences & Pick<UserPreferences, "time_format">) | undefined,
  readable: boolean,
) {
  if (!value) return "—";
  const parts =
    /^(\d{4}-\d{2}-\d{2})[T ](\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})?$/.exec(
      value,
    );
  if (
    !parts ||
    !calendarDate(parts[1]) ||
    +parts[2] > 23 ||
    +parts[3] > 59 ||
    +parts[4] > 59
  )
    return value;
  const date = new Date(value.replace(" ", "T") + (parts[6] ? "" : "Z"));
  if (Number.isNaN(date.getTime())) return value;
  const iso = date.toISOString();
  const fraction = readable ? "" : parts[5] || "";
  const clock =
    preferences?.time_format === "12h"
      ? `${date.getUTCHours() % 12 || 12}${iso.slice(13, readable ? 16 : 19)}${fraction} ${date.getUTCHours() < 12 ? "AM" : "PM"}`
      : `${iso.slice(11, readable ? 16 : 19)}${fraction}`;
  return `${formatDate(iso.slice(0, 10), format, now, preferences)}${readable ? " at " : " · "}${clock} UTC`;
}

/** A recurring calendar boundary has no record year. */
export function formatAnnualDate(month: number, day: number) {
  const value = `2000-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
  const date = calendarDate(value);
  return date
    ? formatter({ month: "long", day: "numeric" }).format(date)
    : value.slice(5);
}

/** A calendar month's name: "October 2026" (long) or "Oct" (short, no year),
 * in the viewer's regional format. */
export function formatMonthName(
  value: string,
  style: "long" | "short" = "long",
  preferences: DatePreferences | undefined = displayPreferences(),
) {
  const date = calendarDate(value.length === 7 ? `${value}-01` : value);
  if (!date) return value || "—";
  const locale = preferences ? wordsLocale(preferences.regional_format) : "en-US";
  return formatter(style === "long" ? { month: "long", year: "numeric" } : { month: "short" }, locale).format(date);
}

/** The browser's own timezone, as an IANA ID; UTC when it can't be read. */
export function browserTimeZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
