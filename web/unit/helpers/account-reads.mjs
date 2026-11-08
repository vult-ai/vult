import { loadComponent } from "./dom.mjs";
const { cashCSV } = await loadComponent("unit/helpers/fixture-csv.ts");
const { parseJournalAccountActivity } = await loadComponent(
  "src/lib/account-hierarchy-activity.ts",
);
const { accountHierarchy, nodeWeeklyActivity, accountBasis } =
  await loadComponent("src/lib/account-hierarchy.ts");
const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
const { creditKind } = await loadComponent("src/lib/v0-ledger.ts");

// Preserve the older CSV fixtures as independent financial oracles while their
// consumers move to scoped JSON reads. These helpers are test transport only.
export function accountReadResponse(url, source, book, accounts) {
  const u = new URL(url, "http://localhost"),
    q = u.searchParams;
  if (
    !["/api/v1/account-hierarchy-activity", "/api/v1/account-detail"].includes(
      u.pathname,
    )
  )
    return null;
  const codes = q.has("codes")
    ? JSON.parse(q.get("codes"))
    : accounts.map((a) => a.code);
  const selected = accounts.filter((a) => codes.includes(a.code));
  const asOf = q.get("as_of") || q.get("to");
  const activity = parseJournalAccountActivity(source, book, asOf);
  const summaries = Object.fromEntries(
    selected.map((a) => [
      a.code,
      activity.accounts[a.code] || {
        weekly: activity.emptyWeekly.map((n) => (n === null ? null : 0)),
        total: 0,
        lastDate: "",
      },
    ]),
  );
  const scopes = new Map();
  const add = (values) => {
    const sorted = [...values].sort();
    scopes.set(sorted.join("\0"), sorted);
  };
  const walk = (nodes) => {
    for (const node of nodes) {
      add(node.codes);
      walk(node.children);
    }
  };
  const roots = accountHierarchy(selected);
  walk(roots);
  for (const a of selected) add([a.code]);
  for (const kind of new Set(roots.map((n) => n.kind)))
    add(roots.filter((n) => n.kind === kind).flatMap((n) => n.codes));
  const hierarchy = {
    version: 1,
    book_id: book.id,
    currency: book.currency,
    scale: book.scale,
    as_of: asOf,
    from: activity.from,
    empty_weekly: activity.emptyWeekly,
    accounts: Object.fromEntries(
      Object.entries(summaries).map(([code, h]) => [
        code,
        { weekly: h.weekly, total: h.total, last_date: h.lastDate },
      ]),
    ),
    groups: [...scopes.values()].map((codes) => {
      const h = nodeWeeklyActivity({ codes }, activity);
      return {
        account_codes: codes,
        weekly: h.weekly,
        count: h.count,
        last_date: h.lastDate,
      };
    }),
    native_balances: Object.fromEntries(
      Object.entries(activity.nativeBalances || {}).filter(([code]) =>
        codes.includes(code),
      ),
    ),
    native_currencies: Object.fromEntries(
      Object.entries(activity.nativeCurrencies || {}).filter(([code]) =>
        codes.includes(code),
      ),
    ),
    content_sha256: "a".repeat(64),
  };
  if (u.pathname.endsWith("account-hierarchy-activity"))
    return Response.json(hierarchy);
  const range = { from: q.get("from"), to: q.get("to") },
    rows = cashCSV(source);
  const basis = accountBasis(selected[0].kind),
    offset = Number(q.get("offset") || 0),
    limit = 100;
  const response = {
    version: 1,
    book_id: book.id,
    currency: book.currency,
    scale: book.scale,
    account_codes: [...codes].sort(),
    ...range,
    basis,
    revision: "a".repeat(64),
    offset,
    limit,
    entries: [],
    earliest_date: "",
    count: 0,
    activity: offset === 0 ? hierarchy : undefined,
  };
  if (q.get("view") === "ledger") {
    const l = accountLedger(book, selected[0], rows, range);
    response.total_minor = String(
      basis === "period" ? l.periodChange : l.closing,
    );
    response.earliest_date =
      rows
        .filter(
          (r) => r.account_code === codes[0] && r.economic_date <= range.to,
        )
        .map((r) => r.economic_date)
        .sort()[0] || "";
    response.count = l.lines.length;
    response.ledger = {
      account_code: codes[0],
      positive_side: l.positiveSide,
      opening_minor: String(l.opening),
      debits_minor: String(l.debits),
      credits_minor: String(l.credits),
      closing_minor: String(l.closing),
      period_change_minor: String(l.periodChange),
      lines: l.lines
        .slice(offset, offset + limit)
        .map((r) => ({
          id: r.id,
          transaction_id: r.transactionId,
          date: r.date,
          posting_date: r.postingDate || "",
          description: r.description,
          memo: r.memo,
          debit_minor: String(r.debit),
          credit_minor: String(r.credit),
          balance_minor: String(r.balance),
        })),
    };
  } else {
    const names = new Map(selected.map((a) => [a.code, a.name])),
      entries = new Map();
    for (const r of rows) {
      if (
        !codes.includes(r.account_code) ||
        r.economic_date > range.to ||
        (basis === "period" && r.economic_date < range.from)
      )
        continue;
      const e = entries.get(r.transaction_id) || {
        id: r.transaction_id,
        date: r.economic_date,
        names: new Set(),
        amount: 0n,
      };
      e.names.add(names.get(r.account_code));
      e.amount +=
        BigInt(r.amount_minor) *
        (r.side === "debit" ? 1n : -1n) *
        (creditKind(selected[0].kind) ? -1n : 1n);
      entries.set(e.id, e);
    }
    const all = [...entries.values()].sort(
      (a, b) => b.date.localeCompare(a.date) || a.id.localeCompare(b.id),
    );
    response.total_minor = String(all.reduce((sum, e) => sum + e.amount, 0n));
    response.count = all.length;
    response.earliest_date = all.at(-1)?.date || "";
    response.entries = all
      .slice(offset, offset + limit)
      .map((e) => ({
        id: e.id,
        date: e.date,
        accounts: [...e.names].join(" · "),
        amount_minor: String(e.amount),
      }));
  }
  return Response.json(response);
}
