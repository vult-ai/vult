import { bookURL, type Book } from "./api";
import { writeHeaders } from "./session";
import {
  activityDate,
  parseAccountActivity,
  type AccountActivity,
  type AccountHistory,
} from "./account-activity";
export { activityFrom } from "./account-activity";
export type { AccountActivity } from "./account-activity";

// The CSV parser remains an independent oracle for imported/exported history.
// The page reads the canonical deduplicated hierarchy projection below.
const dayMS = 86_400_000;
const maxCSV = 32 * 1024 * 1024;
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
/** Use the same parser for an already validated, complete journal detail read. */
export function activityFromJournal(
  book: Pick<Book, "id" | "start_date"> & Partial<Pick<Book, "currency" | "coverage_start_date">>,
  rows: readonly Readonly<Record<string, string>>[],
  asOf: string,
) {
  const fields = rows[0]
    ? Object.keys(rows[0])
    : ["book_id", "transaction_id", "account_code", "economic_date"];
  const cell = (value: string) => `"${value.replace(/"/g, '""')}"`;
  return parseJournalAccountActivity(
    [
      fields.map(cell).join(","),
      ...rows.map((row) => fields.map((key) => cell(row[key] || "")).join(",")),
    ].join("\r\n"),
    book,
    asOf,
  );
}

/** Consume the complete, authenticated journal export; no amounts are interpreted. */
export function parseJournalAccountActivity(
  csv: string,
  book: Pick<Book, "id" | "start_date"> & Partial<Pick<Book, "currency" | "coverage_start_date">>,
  asOf: string,
): AccountActivity {
  if (csv.length > maxCSV)
    throw new Error("Account activity exceeds the supported history size.");
  const end = day(asOf),
    start = end - 52 * 7 + 1;
  let coverage = day(book.coverage_start_date || book.start_date);
  const accounts = new Map<
    string,
    { weekly: number[]; total: number; lastDate: string; seen: Set<string> }
  >();
  const transactionDates = new Map<string, string>();
  let header: string[] | undefined;
  let indexes: Record<string, number> = {};
  let nativeComplete = false;
  const nativeBalances: Record<string, string> = Object.create(null);
  const nativeCurrencies: Record<string, string> = Object.create(null);
  const nativeLines = new Set<string>();
  let records = 0;
  const consume = (row: string[]) => {
    if (!header) {
      header = row;
      for (const key of [
        "book_id",
        "transaction_id",
        "account_code",
        "economic_date",
      ]) {
        if (row.filter((value) => value === key).length !== 1)
          throw new Error("Account history is incomplete or invalid.");
        indexes[key] = row.indexOf(key);
      }
      const nativeFields = [
        "currency",
        "line_id",
        "side",
        "native_currency",
        "native_amount_minor",
      ];
      nativeComplete =
        !!book.currency &&
        nativeFields.every(
          (key) => row.filter((field) => field === key).length === 1,
        );
      if (nativeComplete)
        for (const key of nativeFields) indexes[key] = row.indexOf(key);
      return;
    }
    if (row.length === 1 && row[0] === "") return;
    if (++records > 500_000 || row.length !== header.length)
      throw new Error(
        "Account history is incomplete or exceeds the supported size.",
      );
    if (row[indexes.book_id] !== book.id)
      throw new Error("The account history belongs to another Book.");
    const id = row[indexes.transaction_id],
      code = row[indexes.account_code],
      date = row[indexes.economic_date];
    if (!id || !code || id.length > 128 || code.length > 512)
      throw new Error("Account history has an invalid posting identity.");
    const economicDay = day(date);
    if (transactionDates.has(id) && transactionDates.get(id) !== date)
      throw new Error(
        "Account history contains conflicting transaction dates.",
      );
    transactionDates.set(id, date);
    coverage = Math.min(coverage, economicDay);
    if (economicDay > end) return;
    if (nativeComplete) {
      const line = row[indexes.line_id],
        side = row[indexes.side],
        raw = row[indexes.native_amount_minor],
        currency = row[indexes.native_currency];
      if (
        row[indexes.currency] !== book.currency ||
        !line ||
        nativeLines.has(line) ||
        !["debit", "credit"].includes(side) ||
        (raw !== "" && !/^\d+$/.test(raw)) ||
        Boolean(currency) !== (raw !== "") ||
        (currency &&
          nativeCurrencies[code] &&
          nativeCurrencies[code] !== currency)
      )
        throw new Error("The recorded native balances could not be confirmed.");
      nativeLines.add(line);
      if (currency) {
        nativeCurrencies[code] = currency;
        nativeBalances[code] = (
          BigInt(nativeBalances[code] || "0") +
          BigInt(raw) * (side === "debit" ? 1n : -1n)
        ).toString();
      }
    }
    let account = accounts.get(code);
    if (!account) {
      account = {
        weekly: Array(52).fill(0),
        total: 0,
        lastDate: "",
        seen: new Set(),
      };
      accounts.set(code, account);
    }
    // Split journals may have several lines for the same account and transaction.
    if (account.seen.has(id)) return;
    account.seen.add(id);
    account.total++;
    if (date > account.lastDate) account.lastDate = date;
    if (economicDay >= start)
      account.weekly[Math.floor((economicDay - start) / 7)]++;
  };
  let row: string[] = [],
    field = "",
    quoted = false,
    closed = false;
  const fieldEnd = () => {
    row.push(field);
    field = "";
    closed = false;
    if (row.length > 64) throw new Error("Invalid account history CSV.");
  };
  for (let i = csv.charCodeAt(0) === 0xfeff ? 1 : 0; i < csv.length; i++) {
    const char = csv[i];
    if (quoted) {
      if (char === '"') {
        if (csv[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          quoted = false;
          closed = true;
        }
      } else field += char;
    } else if (char === '"') {
      if (field || closed) throw new Error("Invalid account history CSV.");
      quoted = true;
    } else if (char === ",") fieldEnd();
    else if (char === "\n" || char === "\r") {
      fieldEnd();
      consume(row);
      row = [];
      if (char === "\r" && csv[i + 1] === "\n") i++;
    } else {
      if (closed) throw new Error("Invalid account history CSV.");
      field += char;
    }
    if (field.length > 131_072)
      throw new Error("Account history contains an oversized field.");
  }
  if (quoted) throw new Error("Account history CSV is truncated.");
  if (field || row.length || closed) {
    fieldEnd();
    consume(row);
  }
  if (!header) throw new Error("Account history is missing.");
  const result: Record<string, AccountHistory> = Object.create(null);
  for (const [code, value] of accounts)
    result[code] = {
      weekly: value.weekly.map((count, index) =>
        start + (index + 1) * 7 - 1 < coverage ? null : count,
      ),
      total: value.total,
      lastDate: value.lastDate,
      entries: [...value.seen].map((id) => ({
        id,
        date: transactionDates.get(id)!,
      })),
    };
  return {
    bookId: book.id,
    asOf,
    from: activityDate(start),
    accounts: result,
    emptyWeekly: Array.from({ length: 52 }, (_, index) =>
      start + (index + 1) * 7 - 1 < coverage ? null : 0,
    ),
    nativeBalances: nativeComplete ? nativeBalances : undefined,
    nativeCurrencies: nativeComplete ? nativeCurrencies : undefined,
  };
}

export async function readAccountHierarchyActivity(
  book: Book,
  asOf: string,
  signal: AbortSignal,
  codes?: readonly string[],
) {
  const query = new URLSearchParams({ as_of: asOf });
  const response = await fetchAccountSelection("account-hierarchy-activity", book.id, query, signal, codes);
  if (!response.ok)
    throw new Error("Account activity could not be read. Try again.");
  if (!response.headers.get("content-type")?.toLowerCase().includes("application/json"))
    throw new Error("Account history returned an unexpected response.");
  if (Number(response.headers.get("content-length")) > maxCSV)
    throw new Error("Account activity exceeds the supported history size.");
  return parseHierarchyRead(await response.json(), book, asOf, codes);
}

export function parseHierarchyRead(data: unknown, book: Pick<Book,"id"|"currency"|"scale">, asOf: string, codes?: readonly string[]): AccountActivity {
 const fail = (): never => { throw new Error("The account history could not be confirmed."); };
 if (!data || typeof data!=="object" || Array.isArray(data)) return fail();
 const value=data as Record<string,any>;
 if (value.version!==1 || value.currency!==book.currency || value.scale!==book.scale || !/^[a-f0-9]{64}$/.test(value.content_sha256) || !Array.isArray(value.groups)) return fail();
 const activity = parseAccountActivity(value as Parameters<typeof parseAccountActivity>[0],book,asOf);
 const requested=codes&&[...codes].sort(), actual=Object.keys(activity.accounts).sort();
 if (requested && JSON.stringify(requested)!==JSON.stringify(actual)) return fail();
 const groups=new Map<string,{weekly:(number|null)[];count:number;lastDate:string}>();
 for (const group of value.groups) {
  if (!group || !Array.isArray(group.account_codes) || !group.account_codes.length || group.account_codes.some((code:unknown)=>typeof code!=="string"||!Object.hasOwn(activity.accounts,code)) || new Set(group.account_codes).size!==group.account_codes.length ||
    !Array.isArray(group.weekly) || group.weekly.length!==52 || group.weekly.some((n:unknown,i:number)=>n!==null&&(!Number.isSafeInteger(n)||(n as number)<0)||n===null&&activity.emptyWeekly[i]!==null) ||
    !Number.isSafeInteger(group.count) || group.count<0 || group.weekly.reduce((sum:number,n:number|null)=>sum+(n||0),0)!==group.count ||
    typeof group.last_date!=="string" || (group.last_date!==""&&(!/^\d{4}-\d{2}-\d{2}$/.test(group.last_date)||group.last_date>asOf))) return fail();
  const key=[...group.account_codes].sort().join("\0");
  if(groups.has(key)) return fail();
  groups.set(key,{weekly:group.weekly,count:group.count,lastDate:group.last_date});
 }
 for (const [code,history] of Object.entries(activity.accounts)){
  const group=groups.get(code);
  if(!group||JSON.stringify(group.weekly)!==JSON.stringify(history.weekly)||group.lastDate!==history.lastDate) return fail();
 }
 for(const name of ["native_balances","native_currencies"]){
  if(!value[name]||typeof value[name]!=="object"||Array.isArray(value[name])) return fail();
 }
 if(JSON.stringify(Object.keys(value.native_balances).sort())!==JSON.stringify(Object.keys(value.native_currencies).sort())) return fail();
 for (const [code,amount] of Object.entries(value.native_balances)){
  if(!Object.hasOwn(activity.accounts,code)||typeof amount!=="string"||!/^-?(0|[1-9]\d*)$/.test(amount)||!/^([A-Z]{3})$/.test(value.native_currencies[code])) return fail();
 }
 return {...activity,groups,scope:requested?.join("\0"),stateRevision:value.state_revision,nativeBalances:value.native_balances,nativeCurrencies:value.native_currencies};
}

/** Keep short reads in the query. Larger selections travel in a bounded JSON
 * body, using the same owner session and the POST CSRF proof. */
export async function fetchAccountSelection(
  route: "account-hierarchy-activity" | "account-detail",
  bookId: string,
  query: URLSearchParams,
  signal: AbortSignal,
  codes?: readonly string[],
) {
  const init: RequestInit = { signal };
  if (codes) {
    const ordered = [...codes].sort();
    query.set("codes", JSON.stringify(ordered));
    if (query.toString().length + 64 > 8192) {
      query.delete("codes");
      init.method = "POST";
      init.headers = await writeHeaders(true, signal);
      init.body = JSON.stringify({ codes: ordered });
    }
  }
  return fetch(`/api/v1/${bookURL(`${route}?${query}`, bookId)}`, init);
}

/** Narrows a read of every account to the accounts shown. A group read for
 * more accounts stands for a shown group when every extra account has never
 * recorded an entry, so its figures are the same. Returns null when a shown
 * group has no such stand-in, and the read must name its accounts. */
export function narrowHierarchyActivity(
  read: AccountActivity,
  scope: readonly string[],
  groupsShown: readonly (readonly string[])[],
): AccountActivity | null {
  const accounts: AccountActivity["accounts"] = Object.create(null);
  for (const code of scope) {
    const history = read.accounts[code];
    if (!history) return null;
    accounts[code] = history;
  }
  const unused = (code: string) => {
    const history = read.accounts[code];
    return !!history && history.lastDate === "" && history.weekly.every((n) => !n);
  };
  const all = [...(read.groups || [])];
  const groups = new Map<string, { weekly: (number | null)[]; count: number; lastDate: string }>();
  for (const codes of groupsShown) {
    const key = [...codes].sort().join("\0");
    let group = read.groups?.get(key);
    if (!group) {
      const wanted = new Set(codes);
      group = all.find(([other]) => {
        if (other.length <= key.length) return false;
        const members = other.split("\0");
        const present = new Set(members);
        return codes.every((code) => present.has(code)) && members.every((code) => wanted.has(code) || unused(code));
      })?.[1];
    }
    if (!group) return null;
    groups.set(key, group);
  }
  for (const code of scope) {
    const own = read.groups?.get(code);
    if (own) groups.set(code, own);
  }
  return {
    ...read,
    accounts,
    groups,
    scope: [...scope].sort().join("\0"),
    nativeBalances: read.nativeBalances && Object.fromEntries(Object.entries(read.nativeBalances).filter(([code]) => code in accounts)),
    nativeCurrencies: read.nativeCurrencies && Object.fromEntries(Object.entries(read.nativeCurrencies).filter(([code]) => code in accounts)),
  };
}
