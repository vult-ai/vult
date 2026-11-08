export const dashboardPeriods = [
  { value: "today", label: "Today" },
  { value: "yesterday", label: "Yesterday" },
  { value: "last-7-days", label: "Last 7 days" },
  { value: "last-30-days", label: "Last 30 days" },
  { value: "last-90-days", label: "Last 90 days" },
  { value: "week", label: "This week" },
  { value: "last-week", label: "Last week" },
  { value: "month", label: "This month" },
  { value: "last-month", label: "Last month" },
  { value: "quarter", label: "This quarter" },
  { value: "last-quarter", label: "Last quarter" },
  { value: "year", label: "This year" },
  { value: "last-year", label: "Last year" },
  { value: "year-to-date", label: "Year to date" },
  { value: "last-12-months", label: "Last 12 months" },
  { value: "all", label: "All time" },
] as const;
export type DashboardPeriod = (typeof dashboardPeriods)[number]["value"];
export type DashboardRange = { from: string; to: string; account?: string };
export type DashboardSelection = {
  period: DashboardPeriod | "custom";
  range: DashboardRange;
};

export function isDashboardPeriod(value: unknown): value is DashboardPeriod {
  return dashboardPeriods.some((period) => period.value === value);
}

export function validDashboardRange(value: unknown): value is DashboardRange {
  if (!value || typeof value !== "object") return false;
  const range = value as DashboardRange;
  const validDate = (value: unknown) => {
    if (
      typeof value !== "string" ||
      !/^\d{4}-\d{2}-\d{2}$/.test(value) ||
      value.startsWith("0000-")
    )
      return false;
    const date = new Date(`${value}T00:00:00Z`);
    return (
      Number.isFinite(date.getTime()) &&
      date.toISOString().slice(0, 10) === value
    );
  };
  return validDate(range.from) && validDate(range.to) && range.from <= range.to;
}

export function dashboardPeriodLabel(period: DashboardSelection["period"]) {
  return (
    dashboardPeriods.find((item) => item.value === period)?.label || "Custom"
  );
}

/** Report ranges are date-only values, with one unambiguous year on wide screens. */
export function dashboardRangeLabel(range: DashboardRange, compact = false) {
  const from = new Date(`${range.from}T00:00:00Z`);
  const to = new Date(`${range.to}T00:00:00Z`);
  const format = new Intl.DateTimeFormat("en-US", {
    timeZone: "UTC",
    month: "short",
    day: "numeric",
    ...(!compact || range.from.slice(0, 4) !== range.to.slice(0, 4)
      ? { year: "numeric" as const }
      : {}),
  });
  return (
    range.from === range.to ? format.format(from) : format.formatRange(from, to)
  ).replace(/\s*[–]\s*/g, "–");
}

export function dashboardRange(
  period: DashboardPeriod,
  startDate: string,
  today: string,
): DashboardRange {
  const [year, month] = today.split("-").map(Number);
  const date = (y: number, m: number, day = 1) => {
    const value = new Date("2000-01-01T00:00:00Z");
    value.setUTCFullYear(y, m - 1, day);
    return value.toISOString().slice(0, 10);
  };
  const day = Number(today.slice(8, 10));
  const offset = (days: number) => date(year, month, day + days);
  const monday = -((new Date(`${today}T00:00:00Z`).getUTCDay() + 6) % 7);
  const quarter = Math.floor((month - 1) / 3) * 3 + 1;
  switch (period) {
    case "today":
      return { from: today, to: today };
    case "yesterday":
      return { from: offset(-1), to: offset(-1) };
    // Rolling windows end today, so what is recorded today counts.
    case "last-7-days":
    case "last-30-days":
    case "last-90-days":
      return { from: offset(1 - Number(period.split("-")[1])), to: today };
    case "week":
      return { from: offset(monday), to: offset(monday + 6) };
    case "last-week":
      return { from: offset(monday - 7), to: offset(monday - 1) };
    case "last-month":
      return { from: date(year, month - 1), to: date(year, month, 0) };
    case "quarter":
      return {
        from: date(year, quarter),
        to: date(year, quarter + 3, 0),
      };
    case "last-quarter":
      return { from: date(year, quarter - 3), to: date(year, quarter, 0) };
    case "year":
      return { from: date(year, 1), to: date(year, 12, 31) };
    case "last-year":
      return { from: date(year - 1, 1), to: date(year - 1, 12, 31) };
    case "year-to-date":
      return { from: date(year, 1), to: today };
    case "last-12-months":
      return { from: date(year, month - 12), to: date(year, month, 0) };
    case "all":
      return { from: startDate, to: today };
    default:
      return { from: date(year, month), to: date(year, month + 1, 0) };
  }
}
