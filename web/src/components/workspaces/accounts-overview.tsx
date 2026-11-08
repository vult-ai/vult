import { useState } from "react";
import { CheckCircle, Eye, LayersThree01, Scales02 } from "../../design-system/icons";
import { money, type Book } from "../../lib/api";
import { nativeMoney } from "../../lib/native-money";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { inAccountGroup, type FinancialSnapshot } from "../../lib/v0-ledger";
import type { AccountActivity } from "../../lib/account-activity";
import {
  accountBasis,
  nodeAmount,
  nodeWeeklyActivity,
  type AccountNode,
  type HierarchyAccount,
} from "../../lib/account-hierarchy";
import { Button } from "../ui/button";
import { DashboardPanel } from "../v0/dashboard-panels";
import { Sparkline } from "./account-activity-sparkline";
import "./accounts-overview.css";

const SHOWN = 5;

type Fact = { account: HierarchyAccount; detail: string; amount: string };

/** What the chart holds, before any account is chosen: each class with its
 * figure and weekly activity, the balance check, and accounts worth a look.
 * Everything here is a recorded fact; nothing is judged or inferred. */
export function AccountsOverview({
  book,
  range,
  snapshot,
  roots,
  accounts,
  amounts,
  activity,
  unused,
  filtered,
  nodeName,
  accountName,
  onOpenNode,
  onOpenAccount,
  onShowUnused,
}: {
  book: Book;
  range: { from: string; to: string };
  snapshot: FinancialSnapshot;
  /** The class roots the tree shows. */
  roots: readonly AccountNode[];
  /** The accounts the tree shows. */
  accounts: readonly HierarchyAccount[];
  amounts: ReadonlyMap<string, bigint>;
  /** Weekly activity for the accounts shown; null while it loads. */
  activity: AccountActivity | null;
  /** Accounts that have never recorded an entry, shown or not. */
  unused: number;
  filtered: boolean;
  nodeName: (node: AccountNode) => string;
  accountName: (account: HierarchyAccount) => string;
  onOpenNode: (node: AccountNode, trigger: HTMLElement) => void;
  onOpenAccount: (code: string, trigger: HTMLElement) => void;
  /** Shows every account in the tree, when some are hidden. */
  onShowUnused?: () => void;
}) {
  const through = formatDate(range.to);
  const period = formatDateRange(range.from, range.to);
  const series = activity
    ? roots.map((node) => nodeWeeklyActivity(node, activity).weekly)
    : null;
  const max = Math.max(1, ...(series || []).flat().map((n) => n || 0));

  // The balance check, as the ledger states it.
  const assets = BigInt(snapshot.assets),
    liabilities = BigInt(snapshot.liabilities),
    equity = BigInt(snapshot.equityAccounts),
    result = BigInt(snapshot.accumulatedResult);
  const unclassified = assets - liabilities - equity - result;
  const terms: [string, bigint][] = [
    ["Liabilities", liabilities],
    ["Equity", equity],
    ["Income less expenses", result],
    ...(unclassified ? ([["Accounts without a class", unclassified]] as [string, bigint][]) : []),
  ];

  // Accounts worth a look: facts only.
  const quiet: Fact[] = [],
    negative: Fact[] = [],
    foreign: Fact[] = [],
    classless: Fact[] = [];
  for (const account of accounts) {
    const amount = amounts.get(account.code) || 0n;
    const basis = accountBasis(account.kind);
    const shown = money(amount.toString(), book);
    const last = activity?.accounts[account.code]?.lastDate;
    if (basis === "balance" && amount !== 0n && activity && last !== undefined && (last === "" || last < range.from))
      quiet.push({ account, amount: shown, detail: last ? `Last entry ${formatDate(last)}` : "No entries recorded" });
    // Below zero is news only where it cannot be normal: cash overdrawn, or a card
    // or loan paid past what was owed. Other accounts can sit below zero by design
    // (accumulated depreciation, owner draws, refunds), so they are not flagged.
    if (amount < 0n && (inAccountGroup(account, "cash") || account.kind === "liability"))
      negative.push({ account, amount: shown, detail: account.kind === "liability" ? `Paid past what was owed on ${through}` : `Overdrawn on ${through}` });
    if (account.currency && account.currency !== book.currency && basis === "balance") {
      const native = activity?.nativeBalances?.[account.code];
      foreign.push({
        account,
        amount: native !== undefined ? money(native, { currency: account.currency, scale: account.scale }) : nativeMoney(account),
        detail: `Kept in ${account.currency} · ${shown} in ${book.currency}`,
      });
    }
    if (account.kind === "unknown" && (amount !== 0n || account.posting_count > 0))
      classless.push({ account, amount: shown, detail: "No accounting class recorded" });
  }
  const groups = [
    { id: "quiet", title: "Balance, but no entries this period", detail: `Holding a balance with nothing recorded ${period}.`, facts: quiet },
    { id: "negative", title: "Below zero", detail: "Cash accounts overdrawn, and cards or loans paid past what was owed.", facts: negative },
    { id: "foreign", title: "In another currency", detail: `Kept in their own currency, shown in ${book.currency} elsewhere.`, facts: foreign },
    { id: "classless", title: "Without a class", detail: "Their balances sit outside assets, liabilities, equity, income and expenses.", facts: classless },
  ].filter((group) => group.facts.length);

  return (
    <div className="accounts-overview" data-testid="accounts-overview" role="region" aria-label="Accounts overview">
      <DashboardPanel
        icon={LayersThree01}
        title="Account classes"
        subtitle={`${filtered ? "Filtered · " : ""}Balances as of ${through} · Income and expenses ${period}`}
      >
        <div className="accounts-overview-rows" role="list" aria-label="Account classes">
          {roots.map((node, index) => {
            const flow = accountBasis(node.kind) === "period";
            return (
              <button
                key={node.key}
                type="button"
                role="listitem"
                className="accounts-overview-class vult-focus-target"
                aria-label={`Open ${nodeName(node)}`}
                onClick={(event) => onOpenNode(node, event.currentTarget)}
              >
                <span className="accounts-overview-stack">
                  <span className="vult-type-label text-primary">{nodeName(node)}</span>
                  <span className="vult-type-metadata text-tertiary">
                    {node.codes.length.toLocaleString()} {node.codes.length === 1 ? "account" : "accounts"} · {flow ? "Period total" : "Balance"}
                  </span>
                </span>
                <span className="accounts-overview-spark" aria-hidden={!series || undefined}>
                  {series && activity ? (
                    <Sparkline values={series[index]} max={max} name={nodeName(node)} from={activity.from} to={activity.asOf} shared />
                  ) : null}
                </span>
                <span className="vult-type-ui vult-type-number text-primary accounts-overview-amount">
                  {money(nodeAmount(node, amounts).toString(), book)}
                </span>
              </button>
            );
          })}
        </div>
      </DashboardPanel>

      <DashboardPanel
        icon={Scales02}
        title="Balance check"
        subtitle={`Every recorded entry, in ${book.currency}`}
        action={
          <span className="accounts-overview-check vult-type-metadata">
            <CheckCircle className="size-4" aria-hidden />
            Balanced on {through}
          </span>
        }
      >
        <dl className="accounts-overview-equation" aria-label="Assets equal liabilities, equity and income less expenses">
          {[["Assets", assets] as [string, bigint], ...terms].map(([label, value], index) => (
            <div key={label} className="accounts-overview-term">
              {index > 0 && <span className="accounts-overview-operator vult-type-ui text-tertiary" aria-hidden>{index === 1 ? "=" : "+"}</span>}
              <span className="accounts-overview-stack">
                <dt className="vult-type-metadata text-tertiary">{label}</dt>
                <dd className="vult-type-ui vult-type-number text-primary">{money(value.toString(), book)}</dd>
              </span>
            </div>
          ))}
        </dl>
      </DashboardPanel>

      {/* Waits for the activity read, so its lists arrive together and nothing shifts. */}
      {activity && (groups.length > 0 || unused > 0) && (
        <DashboardPanel icon={Eye} title="Worth a look" subtitle="Recorded facts about single accounts">
          {groups.map((group) => (
            <FactGroup key={group.id} {...group} accountName={accountName} onOpen={onOpenAccount} />
          ))}
          {unused > 0 && (
            <div className="accounts-overview-group" data-group="unused">
              <div className="accounts-overview-group-head">
                <span className="accounts-overview-stack">
                  <span className="accounts-overview-group-title">
                    <span className="vult-type-label text-primary">Never recorded an entry</span>
                    <span className="accounts-overview-count vult-type-metadata text-secondary">{unused.toLocaleString()}</span>
                  </span>
                  <span className="vult-type-metadata text-tertiary">
                    {onShowUnused ? "Hidden from the tree, which shows accounts with activity." : "Shown in the tree with every other account."}
                  </span>
                </span>
                {onShowUnused && (
                  <Button size="sm" color="secondary" onClick={onShowUnused}>Show all accounts</Button>
                )}
              </div>
            </div>
          )}
        </DashboardPanel>
      )}
    </div>
  );
}

function FactGroup({ id, title, detail, facts, accountName, onOpen }: {
  id: string;
  title: string;
  detail: string;
  facts: Fact[];
  accountName: (account: HierarchyAccount) => string;
  onOpen: (code: string, trigger: HTMLElement) => void;
}) {
  const [all, setAll] = useState(false);
  const shown = all ? facts : facts.slice(0, SHOWN);
  return (
    <section className="accounts-overview-group" data-group={id} aria-label={title}>
      <div className="accounts-overview-group-head">
        <span className="accounts-overview-stack">
          <span className="accounts-overview-group-title">
            <span className="vult-type-label text-primary">{title}</span>
            <span className="accounts-overview-count vult-type-metadata text-secondary">{facts.length.toLocaleString()}</span>
          </span>
          <span className="vult-type-metadata text-tertiary">{detail}</span>
        </span>
      </div>
      <ul className="accounts-overview-facts">
        {shown.map(({ account, detail, amount }) => (
          <li key={account.code}>
            <button
              type="button"
              className="accounts-overview-fact vult-focus-target"
              onClick={(event) => onOpen(account.code, event.currentTarget)}
            >
              <span className="accounts-overview-stack">
                <span className="vult-type-ui text-primary">{accountName(account)}</span>
                <span className="vult-type-metadata text-tertiary">{detail}</span>
              </span>
              <span className="vult-type-ui vult-type-number text-primary">{amount}</span>
            </button>
          </li>
        ))}
      </ul>
      {facts.length > SHOWN && (
        <div className="accounts-overview-group-foot">
          <Button size="sm" color="link" onClick={() => setAll((value) => !value)}>
            {all ? "Show fewer" : `Show ${(facts.length - SHOWN).toLocaleString()} more`}
          </Button>
        </div>
      )}
    </section>
  );
}
