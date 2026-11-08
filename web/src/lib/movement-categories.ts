import { DATA_PALETTE } from "../design-system/data-palette";
export type MoneyCategory = {
  id: string;
  label: string;
  direction: "in" | "out";
  total_minor: string;
  series: string[];
};
export type MoneySeriesPoint = {
  date: string;
  money_in_minor: string;
  money_out_minor: string;
};

export type ChartCategory = MoneyCategory & { color: string };
export const MOVEMENT_CHART_SLOTS = 60;
export type MovementBucket = MoneySeriesPoint & { end: string; empty: boolean };

export function movementChartDensity(plotWidth: number) {
  if (plotWidth < 420) return { slots: 24, maxBarWidth: 12 };
  if (plotWidth < 640) return { slots: 36, maxBarWidth: 18 };
  if (plotWidth < 800) return { slots: 40, maxBarWidth: 18 };
  return { slots: plotWidth >= 1280 ? 64 : 60, maxBarWidth: 28 };
}

/** Aggregate exact whole-day evidence for a consistent visual density. Daily
 * records never imply finer time precision merely to fill the chart. */
export function uniformMovementBuckets(
  points: MoneySeriesPoint[],
  categories: MoneyCategory[],
  period: { from: string; to: string },
  targetSlots = MOVEMENT_CHART_SLOTS,
) {
  const first = Date.parse(period.from + "T00:00:00Z");
  const last = Date.parse(period.to + "T00:00:00Z");
  const days = Math.round((last - first) / 86400000) + 1;
  if (!Number.isFinite(days) || days < 1)
    throw new Error("Invalid chart period.");
  if (!Number.isInteger(targetSlots) || targetSlots < 1 || targetSlots > 128)
    throw new Error("Invalid chart density.");
  const slots = Math.min(days, targetSlots);
  const date = (day: number) =>
    new Date(first + day * 86400000).toISOString().slice(0, 10);
  const buckets: MovementBucket[] = Array.from({ length: slots }, (_, i) => {
    const from = Math.floor((i * days) / slots),
      to = Math.floor(((i + 1) * days) / slots);
    return {
      date: date(from),
      end: date(Math.max(from, to - 1)),
      empty: from === to,
      money_in_minor: "0",
      money_out_minor: "0",
    };
  });
  const grouped = categories.map((c) => ({
    ...c,
    series: Array.from({ length: slots }, () => "0"),
  }));
  const seen = new Set<string>();
  for (let i = 0; i < points.length; i++) {
    const point = points[i];
    if (
      seen.has(point.date) ||
      point.date < period.from ||
      point.date > period.to
    )
      throw new Error("Chart evidence is outside its period or duplicated.");
    seen.add(point.date);
    const slot = buckets.findIndex(
      (b) => !b.empty && point.date >= b.date && point.date <= b.end,
    );
    if (slot < 0) throw new Error("Chart movement has no date bucket.");
    for (const key of ["money_in_minor", "money_out_minor"] as const)
      buckets[slot][key] = (
        BigInt(buckets[slot][key]) + BigInt(point[key])
      ).toString();
    for (let j = 0; j < categories.length; j++) {
      if (categories[j].series.length !== points.length)
        throw new Error("Category dates are incomplete.");
      grouped[j].series[slot] = (
        BigInt(grouped[j].series[slot]) + BigInt(categories[j].series[i])
      ).toString();
    }
  }
  return { points: buckets, categories: grouped };
}
/** One shared collection; its leading choices separate neighboring hues. */
export const CHART_CATEGORY_PALETTE = DATA_PALETTE;
export function categoryColor(id: string) {
  if (id === "uncategorized" || id === "other")
    return "var(--color-chart-rose)";
  let hash = 2166136261;
  for (const char of id)
    hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
  return `var(--color-chart-${CHART_CATEGORY_PALETTE[(hash >>> 0) % CHART_CATEGORY_PALETTE.length]})`;
}

/** Keep the leading categories and an exact Other remainder. Identity and color
 * come from the category, never its rank in the current time window. */
export function chartCategories(
  categories: MoneyCategory[],
  points: MoneySeriesPoint[],
  direction: "in" | "out",
): ChartCategory[] {
  const sorted = categories
    .filter((c) => c.direction === direction && BigInt(c.total_minor) > 0n)
    .sort((a, b) => {
      const difference = BigInt(b.total_minor) - BigInt(a.total_minor);
      return difference === 0n
        ? a.id.localeCompare(b.id)
        : difference > 0n
          ? 1
          : -1;
    });
  for (const c of sorted) {
    if (
      c.series.length !== points.length ||
      c.series.reduce((n, v) => n + BigInt(v), 0n) !== BigInt(c.total_minor)
    )
      throw new Error("Category timeline does not match its total.");
  }
  const leading = sorted.slice(0, 4);
  const rest = sorted.slice(4);
  if (rest.length)
    leading.push({
      id: "other",
      label: "Other",
      direction,
      total_minor: rest
        .reduce((n, c) => n + BigInt(c.total_minor), 0n)
        .toString(),
      series: points.map((_, i) =>
        rest.reduce((n, c) => n + BigInt(c.series[i]), 0n).toString(),
      ),
    });
  return leading.map((c) => ({ ...c, color: categoryColor(c.id) }));
}

export function categoryShare(amount: string, total: string) {
  const value = BigInt(amount),
    sum = BigInt(total);
  if (sum <= 0n) return "0%";
  if (value > 0n && value * 1000n < sum) return "<0.1%";
  return `${Number((value * 1000n + sum / 2n) / sum) / 10}%`;
}
