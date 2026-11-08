import type { Book, State } from "./api";
import { financialSnapshot } from "./v0-ledger";

export type OverviewPoint = {
  date: string;
  assets: string;
  liabilities: string;
  equity: string;
};

export type OverviewComparison = {
  current: OverviewPoint[];
  previous: OverviewPoint[];
  range: { from: string; to: string };
  previousRange?: { from: string; to: string };
};

/** Equal-length recorded windows; dates before this Book begins stay absent. */
export function overviewComparison(
  book: Book,
  state: State,
  journal: Record<string, string>[],
  range: { from: string; to: string },
): OverviewComparison {
  const day = 86400000;
  const start = Date.parse(range.from + "T00:00:00Z");
  const end = Date.parse(range.to + "T00:00:00Z");
  if (!Number.isFinite(start) || !Number.isFinite(end) || start > end)
    throw new Error("The trend dates are invalid.");
  const iso = (value: number) => new Date(value).toISOString().slice(0, 10);
  const length = end - start + day;
  const previousRange = { from: iso(start - length), to: iso(start - day) };
  const sample = (from: number, to: number) => {
    const count = Math.min(14, Math.round((to - from) / day) + 1);
    return Array.from({ length: count }, (_, i) =>
      iso(
        from +
          (count === 1
            ? 0
            : Math.round((i * (to - from)) / ((count - 1) * day)) * day),
      ),
    )
      .filter((date) => date >= (book.coverage_start_date || book.start_date))
      .map((date) => {
        const { assets, liabilities, equity } = financialSnapshot(
          book,
          state,
          journal,
          { from: (book.coverage_start_date || book.start_date), to: date },
        );
        return { date, assets, liabilities, equity };
      });
  };
  return {
    range,
    previousRange,
    current: sample(start, end),
    previous: sample(start - length, start - day),
  };
}

/** Small, read-only samples of recorded position; never bank observations. */
export function overviewHistory(
  book: Book,
  state: State,
  journal: Record<string, string>[],
  through: string,
): OverviewPoint[] {
  const from = (book.coverage_start_date || book.start_date);
  if (from > through) return [];
  const start = Date.parse(from + "T00:00:00Z");
  const end = Date.parse(through + "T00:00:00Z");
  const days = Math.round((end - start) / 86400000);
  if (!Number.isFinite(days) || days < 0) return [];
  const count = Math.min(14, days + 1);
  return Array.from({ length: count }, (_, i) => {
    const day = count === 1 ? 0 : Math.round((i * days) / (count - 1));
    const date = new Date(start + day * 86400000).toISOString().slice(0, 10);
    const { assets, liabilities, equity } = financialSnapshot(
      book,
      state,
      journal,
      { from, to: date },
    );
    return { date, assets, liabilities, equity };
  });
}
