import type { Detail, State } from "./api";
import { formatDate } from "./date-format";
import { transactionFacts, type JournalPosting } from "./transaction-journal";

export type EntryMoneyStep = {
  key: string;
  kind: "account" | "counterparty";
  name: string;
  label: string;
  card?: boolean;
};

/** Project the selected entry's own postings; never infer an account or source
 * from merchant text, notes, a current balance or account-code prefixes.
 */
export function entryOverview(data: Detail, state: State) {
  const chart = new Map(
    state.accounts.map((account) => [account.code, account]),
  );
  for (const line of data.lines)
    if (!chart.has(line.account_code) && line.account_name)
      chart.set(line.account_code, {
        code: line.account_code,
        name: line.account_name,
        kind: "",
      });
  const cash = new Set([
    ...(state.cash_accounts || []).map((account) => account.code),
    ...(state.account_balances || [])
      .filter((account) => account.usage === "cash")
      .map((account) => account.code),
    ...[data.fx.cash_account, data.fx.destination_account].filter(
      (code): code is string => !!code,
    ),
  ]);
  const postings: JournalPosting[] = data.lines.map((line) => ({
    id: line.id,
    account: line.account_code,
    amount: BigInt(line.amount_minor) * (line.side === "debit" ? 1n : -1n),
  }));
  const facts = transactionFacts(
    { ...state, accounts: [...chart.values()] },
    new Map([[data.id, postings]]),
    cash,
  )(data);
  const net = new Map<string, bigint>();
  for (const line of postings)
    net.set(line.account, (net.get(line.account) || 0n) + line.amount);
  const cashNet = [...net]
    .filter(([code]) => cash.has(code))
    .reduce((sum, [, amount]) => sum + amount, 0n);
  const transfer = facts.direction === "transfer";
  const accounts = [...net].flatMap(([code, amount]) => {
    const card = code === data.fx.card_account;
    if ((!cash.has(code) && !card) || amount === 0n) return [];
    const name =
      chart.get(code)?.name ||
      state.cash_accounts?.find((account) => account.code === code)?.name;
    if (!name || name === code) return [];
    return [
      {
        code,
        name,
        label: card
          ? "Card"
          : transfer
            ? amount < 0n
              ? "From"
              : "To"
            : "Account",
      },
    ];
  });
  if (transfer)
    accounts.sort(
      (a, b) => (a.label === "From" ? 0 : 1) - (b.label === "From" ? 0 : 1),
    );
  const label =
    data.kind === "reversal"
      ? "Reversal"
      : transfer
        ? "Transfer"
        : data.kind === "card_purchase"
          ? "Card purchase"
          : data.kind === "card_refund"
            ? "Refund"
            : cashNet > 0n
              ? "Received"
              : cashNet < 0n
                ? "Paid"
                : data.kind === "journal" && !data.view_kind
                  ? "Adjustment"
                  : facts.directionLabel;
  const accountStep = (
    account: (typeof accounts)[number],
    direction: string,
  ): EntryMoneyStep => ({
    key: `account:${account.code}`,
    kind: "account",
    name: account.name,
    label:
      account.label === "Card"
        ? data.kind === "card_purchase"
          ? "Charged to"
          : data.kind === "card_refund"
            ? "Refunded to"
            : "Card"
        : direction,
    card: account.label === "Card",
  });
  const party = data.counterparty?.trim();
  const partyStep = (direction: string): EntryMoneyStep[] =>
    party
      ? [
          {
            key: "counterparty",
            kind: "counterparty",
            name: party,
            label: direction,
          },
        ]
      : [];
  const outgoing = accounts.filter(({ code }) => net.get(code)! < 0n);
  const incoming = accounts.filter(({ code }) => net.get(code)! > 0n);
  // Only the selected entry's identified funding postings establish a path.
  // P&L direction or merchant text alone cannot establish a cash movement.
  const path: EntryMoneyStep[] =
    data.kind === "reversal"
      ? [
          ...accounts.map((account) => accountStep(account, "Account")),
          ...partyStep("Counterparty"),
        ]
      : transfer
        ? [
            ...outgoing.map((account) => accountStep(account, "From")),
            ...incoming.map((account) => accountStep(account, "To")),
          ]
        : outgoing.length && !incoming.length
          ? [
              ...outgoing.map((account) => accountStep(account, "From")),
              ...partyStep("To"),
            ]
          : incoming.length && !outgoing.length
            ? [
                ...partyStep("From"),
                ...incoming.map((account) => accountStep(account, "To")),
              ]
            : [
                ...accounts.map((account) => accountStep(account, "Account")),
                ...partyStep("Counterparty"),
              ];
  return {
    label,
    direction: label === "Adjustment" ? ("neutral" as const) : facts.direction,
    category: !transfer && facts.category !== "—" ? facts.category : "",
    categoryTitle: facts.categoryTitle,
    categoryAccount: facts.categoryAccount,
    accounts,
    path,
  };
}

/** Separate an explicitly masked suffix for layout, preserving its exact bytes. */
export function entryAccountIdentity(name: string) {
  const masked = name.match(/^(.*?)\s+([•●*·]{2,}\s*\d[\d\s-]*)$/u);
  return {
    name: masked?.[1].trim() || name,
    suffix: masked?.[1].trim() ? masked[2].trim() : "",
  };
}

export function entryDisplayDate(date: string) {
  return formatDate(date, "full");
}

/** The memo every posting carries, when they all carry the same one and it
 * says something the description does not. Shown once, not on each line. */
export function sharedMemo(lines: readonly { memo?: string }[], description: string) {
  const memos = new Set(lines.map((line) => (line.memo || "").trim()));
  const [memo] = memos;
  return memos.size === 1 && memo && memo !== description.trim() ? memo : "";
}
