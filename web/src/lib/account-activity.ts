import { bookURL, type Book } from "./api";

const dayMS = 86_400_000;
export type AccountHistory = {
  weekly: (number | null)[];
  total: number;
  lastDate: string;
  entries?: { id: string; date: string }[];
};
export type AccountActivity = {
  bookId: string;
  asOf: string;
  from: string;
  accounts: Record<string, AccountHistory>;
  emptyWeekly: (number | null)[];
  nativeBalances?: Record<string, string>;
  nativeCurrencies?: Record<string, string>;
  groups?: ReadonlyMap<string, { weekly: (number | null)[]; count: number; lastDate: string }>;
  scope?: string;
  stateRevision?: string;
};

function day(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value))
    throw new Error("Invalid activity date.");
  const time = Date.parse(value + "T00:00:00Z");
  if (
    !Number.isFinite(time) ||
    new Date(time).toISOString().slice(0, 10) !== value
  )
    throw new Error("Invalid activity date.");
  return time / dayMS;
}
export const activityDate = (value: number) =>
  new Date(value * dayMS).toISOString().slice(0, 10);
export function activityFrom(asOf: string, weeks: number) {
  return activityDate(day(asOf) - weeks * 7 + 1);
}

type ActivityResponse = {
  book_id: string;
  as_of: string;
  from: string;
  accounts: Record<
    string,
    { weekly: (number | null)[]; total: number; last_date: string }
  >;
  empty_weekly: (number | null)[];
};
const count = (n: unknown): n is number =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0;
const weekly = (v: unknown): v is (number | null)[] =>
  Array.isArray(v) && v.length === 52 && v.every((n) => n === null || count(n));

export function parseAccountActivity(
  data: ActivityResponse,
  book: Pick<Book, "id">,
  asOf: string,
): AccountActivity {
  if (!data || typeof data !== "object")
    throw Error("Account history is incomplete or invalid.");
  if (data.book_id !== book.id)
    throw Error("The account history belongs to another Book.");
  if (
    data.as_of !== asOf ||
    data.from !== activityFrom(asOf, 52) ||
    !weekly(data.empty_weekly) ||
    !data.accounts ||
    typeof data.accounts !== "object" ||
    Array.isArray(data.accounts)
  )
    throw Error("Account history is incomplete or invalid.");
  const accounts: Record<string, AccountHistory> = Object.create(null);
  for (const [code, value] of Object.entries(data.accounts)) {
    if (
      !value ||
      !weekly(value.weekly) ||
      !count(value.total) ||
      typeof value.last_date !== "string" ||
      value.last_date > asOf
    )
      throw Error("Account history is incomplete or invalid.");
    if (value.last_date) day(value.last_date);
    else if (value.total !== 0 || value.weekly.some((n) => (n || 0) > 0))
      throw Error("Account history is incomplete or invalid.");
    accounts[code] = {
      weekly: value.weekly,
      total: value.total,
      lastDate: value.last_date,
    };
  }
  return {
    bookId: book.id,
    asOf,
    from: data.from,
    accounts,
    emptyWeekly: data.empty_weekly,
  };
}

export async function readAccountActivity(
  book: Book,
  asOf: string,
  signal: AbortSignal,
) {
  const response = await fetch(
    `/api/v1/${bookURL(`account-activity?as_of=${encodeURIComponent(asOf)}`, book.id)}`,
    { signal },
  );
  if (!response.ok)
    throw Error("Account activity could not be read. Try again.");
  if (
    !response.headers
      .get("content-type")
      ?.toLowerCase()
      .includes("application/json")
  )
    throw Error("Account history returned an unexpected response.");
  return parseAccountActivity(await response.json(), book, asOf);
}
