import { useRef, useState, type CSSProperties, type ReactNode } from "react";
import {
  Tree,
  TreeItem,
  TreeItemContent,
  Button as TreeButton,
} from "react-aria-components";
import {
  ChevronRight,
  Wallet01,
  CreditCard01,
  PieChart02,
  Coins01,
  Receipt,
} from "../../design-system/icons";
import { money, type Book } from "../../lib/api";
import {
  accountBasis,
  nodeAmount,
  hierarchyRows,
  type AccountNode,
  type HierarchyAccount,
} from "../../lib/account-hierarchy";
import { Tooltip } from "../../design-system/catalog/components/base/tooltip/tooltip";
import "./account-tree.css";
import { EmptyState } from "../ui/empty-state";

type Props = {
  scrollRef?: (element: HTMLDivElement | null) => void;
  roots: readonly AccountNode[];
  flat?: readonly HierarchyAccount[];
  book: Book;
  amounts: ReadonlyMap<string, bigint> | null;
  expanded: ReadonlySet<string>;
  onExpanded: (keys: Set<string>) => void;
  compare: (a: AccountNode, b: AccountNode) => number;
  nodeName: (node: AccountNode) => string;
  accountName: (account: HierarchyAccount) => string;
  accountSuffix: (account: HierarchyAccount) => string;
  icon: (account: HierarchyAccount) => ReactNode;
  customize: (account: HierarchyAccount) => ReactNode;
  grip: (account: HierarchyAccount) => ReactNode;
  partial: (node: AccountNode) => boolean;
  onOpen: (node: AccountNode, trigger: HTMLElement) => void;
  selected?: string;
  contextId: string;
};

const classifications = [
  ["asset", "Assets"],
  ["liability", "Liabilities"],
  ["equity", "Equity"],
  ["revenue", "Income"],
  ["expense", "Expenses"],
] as const;

/** A presentation of the existing chart; these roots are never ledger accounts. */
export function classifiedAccounts(
  roots: readonly AccountNode[],
): AccountNode[] {
  return classifications
    .flatMap<AccountNode>(([kind, name]) => {
      const children = roots.filter((node) => node.kind === kind);
      return children.length
        ? [
            {
              key: JSON.stringify(["classification", kind]),
              name,
              kind,
              own: [],
              children,
              codes: children.flatMap((node) => node.codes),
            },
          ]
        : [];
    })
    .concat(
      roots.filter(
        (node) => !classifications.some(([kind]) => kind === node.kind),
      ),
    );
}

function AccountTreeRowContent({
  depth,
  leading,
  label,
  suffix,
  extra,
  title,
  amount,
  actionLabel,
  isDisabled,
  onOpen,
}: {
  depth: number;
  leading: ReactNode;
  label: ReactNode;
  suffix?: string;
  extra?: ReactNode;
  title: string;
  amount: string;
  actionLabel: string;
  isDisabled: boolean;
  onOpen: () => void;
}) {
  const labelRef = useRef<HTMLSpanElement>(null);
  const [showHelp, setShowHelp] = useState(false);
  const reveal = () => {
    const text = labelRef.current;
    setShowHelp(!!text && text.scrollWidth > text.clientWidth);
  };
  return (
    <div
      className="account-tree-content"
      style={{ "--account-depth": depth } as CSSProperties}
    >
      <span className="account-tree-identity">
        {leading}
        <span
          className="account-tree-label"
          ref={labelRef}
          onMouseEnter={reveal}
          onMouseLeave={() => setShowHelp(false)}
        >
          <span className="account-tree-name">{label}</span>
          {suffix && <span className="account-tree-suffix">{suffix}</span>}
        </span>
        {extra}
      </span>
      <Tooltip
        title={title}
        placement="right"
        motion="none"
        isOpen={showHelp}
        onOpenChange={(open) => (open ? reveal() : setShowHelp(false))}
      >
        <TreeButton
          className="account-tree-amount"
          aria-label={actionLabel}
          isDisabled={isDisabled}
          onFocus={reveal}
          onBlur={() => setShowHelp(false)}
          onPress={() => {
            setShowHelp(false);
            onOpen();
          }}
        >
          {amount}
        </TreeButton>
      </Tooltip>
    </div>
  );
}

export function AccountTree(props: Props) {
  const elements = useRef(new Map<string, HTMLDivElement>());
  const broadIcons: Record<string, typeof Wallet01> = {
    asset: Wallet01,
    liability: CreditCard01,
    equity: PieChart02,
    revenue: Coins01,
    expense: Receipt,
  };
  const shellFirst = new Set<string>(),
    shellLast = new Set<string>();
  const branchGuides = new Map<
    string,
    { level: number; branch: boolean; last: boolean; tail: number }[]
  >();
  if (!props.flat)
    for (const root of props.roots) {
      const visible = hierarchyRows([root], props.expanded, props.compare);
      const rowId = (row: (typeof visible)[number]) =>
        row.account ? `account:${row.account.code}` : `node:${row.node.key}`;
      if (visible.length) {
        shellFirst.add(rowId(visible[0]));
        shellLast.add(rowId(visible[visible.length - 1]));
      }
      visible.forEach((parent, index) => {
        if (parent.account || !props.expanded.has(parent.node.key)) return;
        let end = index + 1;
        const children: number[] = [];
        while (end < visible.length && visible[end].depth > parent.depth) {
          if (visible[end].depth === parent.depth + 1) children.push(end);
          end++;
        }
        children.forEach((child, childIndex) => {
          const last = childIndex === children.length - 1;
          const until = last ? child + 1 : children[childIndex + 1];
          for (let row = child; row < until; row++) {
            const id = rowId(visible[row]);
            const guides = branchGuides.get(id) || [];
            guides.push({
              level: parent.depth,
              branch: row === child,
              last,
              tail: visible[row].account ? 16 : 4,
            });
            branchGuides.set(id, guides);
          }
        });
      });
    }
  const guides = (id: string) =>
    branchGuides.get(id)?.map((guide) => (
      <svg
        key={guide.level}
        aria-hidden="true"
        focusable="false"
        className="account-tree-guide"
        viewBox="0 0 20 40"
        fill="none"
        stroke="currentColor"
        strokeWidth="1"
        strokeLinecap="round"
        strokeLinejoin="round"
        data-branch={guide.branch || undefined}
        data-last={guide.last || undefined}
        style={{ "--guide-level": guide.level } as CSSProperties}
      >
        {!guide.last && <path d="M0.5 0V40" />}
        {guide.branch && (
          <path
            d={`${guide.last ? "M0.5 0V16" : "M0.5 16"} Q0.5 20 4.5 20 H${guide.tail + 0.5}`}
          />
        )}
      </svg>
    ));
  const open = (node: AccountNode, id: string) => {
    const trigger = elements.current.get(id);
    if (trigger && props.amounts) props.onOpen(node, trigger);
  };
  function leaf(
    account: HierarchyAccount,
    node: AccountNode,
    depth: number,
    direct = false,
  ) {
    const id = `account:${account.code}`,
      name = props.accountName(account);
    const detail = {
      ...node,
      key: id,
      name,
      own: [account],
      children: [],
      codes: [account.code],
    };
    const label = direct
      ? "Direct postings"
      : props.flat
        ? account.name === name
          ? name
          : `${name} · ${account.name}`
        : name;
    const suffix = props.accountSuffix(account);
    const fullLabel = `${account.gl_code ? account.gl_code + " · " : ""}${label}${suffix && !props.flat ? " " + suffix : ""}`;
    const amount = props.amounts
      ? money((props.amounts.get(account.code) || 0n).toString(), props.book)
      : "—";
    return (
      <TreeItem
        key={id}
        id={id}
        textValue={`${name} ${node.own.map((a) => a.gl_code || "").join(" ")}`}
        className="account-tree-row"
        style={{ "--account-depth": depth } as CSSProperties}
        ref={(element) => {
          if (element) elements.current.set(id, element);
          else elements.current.delete(id);
        }}
        data-account-code={account.code}
        data-group-shell={!props.flat || undefined}
        data-group-first={shellFirst.has(id) || undefined}
        data-group-last={shellLast.has(id) || undefined}
        data-account-selected={props.selected === id || undefined}
        aria-label={`${account.gl_code ? account.gl_code + " · " : ""}${account.name}, ${amount}, ${accountBasis(account.kind) === "period" ? "period total" : "balance as of selected date"}`}
        onAction={() => open(detail, id)}
      >
        <TreeItemContent>
          <AccountTreeRowContent
            depth={depth}
            leading={
              <>
                {guides(id)}
                {props.flat ? (
                  props.grip(account)
                ) : (
                  <span className="account-tree-chevron-space" />
                )}
              </>
            }
            label={
              <>
                {account.gl_code && (
                  <span className="account-tree-gl-code">
                    {account.gl_code} ·{" "}
                  </span>
                )}
                {label}
              </>
            }
            suffix={!props.flat ? suffix : undefined}
            extra={
              <span className="account-tree-customize">
                {props.customize(account)}
              </span>
            }
            title={fullLabel}
            amount={amount}
            actionLabel={`View ${name} account details`}
            isDisabled={!props.amounts}
            onOpen={() => open(detail, id)}
          />
        </TreeItemContent>
      </TreeItem>
    );
  }
  function branch(node: AccountNode, depth: number): ReactNode[] {
    if (!node.children.length)
      return node.own.map((account) => leaf(account, node, depth));
    const id = `node:${node.key}`,
      name = props.nodeName(node);
    const amount = props.amounts
      ? money(nodeAmount(node, props.amounts).toString(), props.book)
      : "—";
    const fullLabel = `${node.own.length === 1 && node.own[0].gl_code ? node.own[0].gl_code + " · " : ""}${name}${props.partial(node) ? " · Filtered" : ""}`;
    const CategoryIcon = depth === 0 ? broadIcons[node.kind] : undefined;
    return [
      <TreeItem
        key={id}
        id={id}
        textValue={`${name} ${node.own.map((a) => a.gl_code || "").join(" ")}`}
        className="account-tree-row account-tree-parent"
        style={{ "--account-depth": depth } as CSSProperties}
        ref={(element) => {
          if (element) elements.current.set(id, element);
          else elements.current.delete(id);
        }}
        data-account-group={node.key}
        data-group-shell={!props.flat || undefined}
        data-group-first={shellFirst.has(id) || undefined}
        data-group-last={shellLast.has(id) || undefined}
        data-account-selected={props.selected === node.key || undefined}
        aria-label={`${name}, ${amount}${props.partial(node) ? ", filtered total" : ""}`}
        onAction={() => open(node, id)}
      >
        <TreeItemContent>
          {({ isExpanded }) => (
            <AccountTreeRowContent
              depth={depth}
              leading={
                <>
                  {guides(id)}
                  <TreeButton
                    slot="chevron"
                    className="account-tree-chevron"
                    aria-label={isExpanded ? "Collapse" : "Expand"}
                  >
                    <ChevronRight aria-hidden="true" />
                  </TreeButton>
                  {CategoryIcon && (
                    <span className="account-tree-icon">
                      <CategoryIcon
                        className="account-tree-category"
                        aria-hidden="true"
                      />
                    </span>
                  )}
                </>
              }
              label={
                <>
                  {node.own.length === 1 &&
                    node.own[0].gl_code && (
                      <span className="account-tree-gl-code">
                        {node.own[0].gl_code} ·{" "}
                      </span>
                    )}
                  {name}
                </>
              }
              suffix={props.partial(node) ? "Filtered" : undefined}
              title={fullLabel}
              amount={amount}
              actionLabel={`View ${name} combined activity`}
              isDisabled={!props.amounts}
              onOpen={() => open(node, id)}
            />
          )}
        </TreeItemContent>
        {node.own
          .filter((account) => account.posting_count > 0)
          .map((account) => leaf(account, node, depth + 1, true))}
        {[...node.children]
          .sort(props.compare)
          .flatMap((child) => branch(child, depth + 1))}
      </TreeItem>,
    ];
  }
  return (
    <div
      className="account-tree-frame"
      data-testid="account-tree"
      data-flat={!!props.flat || undefined}
    >
      <Tree
        ref={props.scrollRef}
        aria-label="Chart of accounts"
        aria-describedby={props.contextId}
        className="account-tree"
        selectionMode="none"
        // Item actions open details; suppress the tree's default row expansion.
        onAction={() => {}}
        expandedKeys={new Set([...props.expanded].map((key) => `node:${key}`))}
        onExpandedChange={(keys) =>
          props.onExpanded(
            new Set(
              [...keys]
                .map(String)
                .filter((key) => key.startsWith("node:"))
                .map((key) => key.slice(5)),
            ),
          )
        }
        renderEmptyState={() => (
          <EmptyState
            scope="region"
            title="No accounts in this view"
            detail="Adjust the search or filters."
          />
        )}
      >
        {props.flat
          ? props.flat.map((account) =>
              leaf(
                account,
                {
                  key: account.code,
                  name: account.name,
                  kind: account.kind,
                  own: [account],
                  children: [],
                  codes: [account.code],
                },
                0,
              ),
            )
          : props.roots.flatMap((node) => branch(node, 0))}
      </Tree>
    </div>
  );
}
