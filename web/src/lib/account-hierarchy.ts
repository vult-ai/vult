import type { State } from "./api";
import type { AccountActivity } from "./account-activity";
import type { FinancialSnapshot } from "./v0-ledger";

export type HierarchyAccount = NonNullable<State["account_balances"]>[number];
export type AccountNode = {
  key: string;
  name: string;
  kind: string;
  own: HierarchyAccount[];
  children: AccountNode[];
  codes: string[];
};

export const accountBasis = (kind: string) =>
  ["revenue", "income", "other_income", "expense", "other_expense"].includes(
    kind,
  )
    ? ("period" as const)
    : ("balance" as const);
const broadKind = (kind: string) =>
  ["revenue", "income", "other_income"].includes(kind)
    ? "revenue"
    : ["expense", "other_expense"].includes(kind)
      ? "expense"
      : ["asset", "other_asset", "fixed_asset"].includes(kind)
        ? "asset"
        : kind;

/** Parents come only from recorded parent links (`parent_id`, when the Book
 * stores them). Vult Open's basis is `flat_recorded_accounts`: names containing
 * colons or other separators never establish parents. A link to a missing
 * account, across account classes or in a cycle is ignored. */
export function accountHierarchy(accounts: readonly HierarchyAccount[]) {
  const roots: AccountNode[] = [],
    nodes = new Map<string, AccountNode>();
  const byCode = new Map<string, HierarchyAccount>();
  for (const account of accounts) {
    if (byCode.has(account.code))
      throw new Error("The account list contains a duplicate identity.");
    byCode.set(account.code, account);
  }
  const parentOf = (account: HierarchyAccount) => {
    const parent = account.parent_id ? byCode.get(account.parent_id) : undefined;
    return parent && broadKind(parent.kind) === broadKind(account.kind) ? parent : undefined;
  };
  const chain = (account: HierarchyAccount) => {
    const path = [account];
    const seen = new Set([account.code]);
    for (let p = parentOf(account); p && !seen.has(p.code); p = parentOf(p)) {
      seen.add(p.code);
      path.unshift(p);
    }
    // A cycle leaves the account at the root rather than inventing an order.
    const last = parentOf(path[0]);
    return last && seen.has(last.code) ? [account] : path;
  };
  for (const account of accounts) {
    let siblings = roots;
    const path = chain(account);
    path.forEach((step, index) => {
      const kind = broadKind(step.kind),
        key = JSON.stringify([kind, step.code]);
      let node = nodes.get(key);
      if (!node) {
        node = { key, name: step.name, kind, own: [], children: [], codes: [] };
        nodes.set(key, node);
        siblings.push(node);
      }
      node.codes.push(account.code);
      if (index === path.length - 1) node.own.push(account);
      siblings = node.children;
    });
  }
  return roots;
}

/** Projection rows are direct-account figures. A missing row is confirmed zero. */
export function accountAmounts(
  snapshot: FinancialSnapshot,
  accounts: readonly HierarchyAccount[],
) {
  const positions = new Map(
    snapshot.rows.map((row) => [row.code, BigInt(row.amount)]),
  );
  const flows = new Map(
    [...snapshot.incomeRows, ...snapshot.expenseRows].map((row) => [
      row.code,
      BigInt(row.amount),
    ]),
  );
  return new Map(
    accounts.map((account) => [
      account.code,
      (accountBasis(account.kind) === "period" ? flows : positions).get(
        account.code,
      ) || 0n,
    ]),
  );
}

export function nodeAmount(
  node: AccountNode,
  amounts: ReadonlyMap<string, bigint>,
) {
  return node.codes.reduce(
    (total, code) => total + (amounts.get(code) || 0n),
    0n,
  );
}

export function hierarchyIndex(roots: readonly AccountNode[]) {
  const result = new Map<string, AccountNode>();
  const visit = (nodes: readonly AccountNode[]) => {
    for (const node of nodes) {
      result.set(node.key, node);
      visit(node.children);
    }
  };
  visit(roots);
  return result;
}

/** Distinct transactions across all members; split entries are counted once. */
export function nodeActivity(node: AccountNode, activity: AccountActivity) {
  const scoped = activity.groups?.get([...node.codes].sort().join("\0"));
  if (scoped) return { count: scoped.count, lastDate: scoped.lastDate };
  const entries = new Map<string, string>();
  for (const code of node.codes)
    for (const entry of activity.accounts[code]?.entries || [])
      entries.set(entry.id, entry.date);
  let count = 0,
    lastDate = "";
  for (const date of entries.values()) {
    if (date > lastDate) lastDate = date;
    if (date >= activity.from && date <= activity.asOf) count++;
  }
  return { count, lastDate };
}

/** Evidence-backed weekly series for the selected account/group only. */
export function nodeWeeklyActivity(
  node: AccountNode,
  activity: AccountActivity,
) {
  const scoped = activity.groups?.get([...node.codes].sort().join("\0"));
  if (scoped) return scoped;
  const entries = new Map<string, string>();
  for (const code of node.codes)
    for (const entry of activity.accounts[code]?.entries || [])
      entries.set(entry.id, entry.date);
  const weekly: (number | null)[] = activity.emptyWeekly.map((value) =>
    value === null ? null : 0,
  );
  for (const date of entries.values()) {
    if (date < activity.from || date > activity.asOf) continue;
    const index = Math.floor(
      (Date.parse(date + "T00:00:00Z") -
        Date.parse(activity.from + "T00:00:00Z")) /
        (7 * 86400000),
    );
    weekly[index] = (weekly[index] || 0) + 1;
  }
  return { weekly, ...nodeActivity(node, activity) };
}

export type AccountTreeRow = {
  key: string;
  depth: number;
  node: AccountNode;
  account?: HierarchyAccount;
  direct?: boolean;
};
export function hierarchyRows(
  roots: readonly AccountNode[],
  expanded: ReadonlySet<string>,
  compare: (a: AccountNode, b: AccountNode) => number,
) {
  const result: AccountTreeRow[] = [];
  const visit = (nodes: readonly AccountNode[], depth: number) => {
    for (const node of [...nodes].sort(compare)) {
      if (node.children.length) {
        result.push({ key: node.key, depth, node });
        if (!expanded.has(node.key)) continue;
        for (const account of node.own)
          if (account.posting_count > 0)
            result.push({
              key: account.code,
              depth: depth + 1,
              node,
              account,
              direct: true,
            });
        visit(node.children, depth + 1);
      } else {
        for (const account of node.own)
          result.push({ key: account.code, depth, node, account });
      }
    }
  };
  visit(roots, 0);
  return result;
}
