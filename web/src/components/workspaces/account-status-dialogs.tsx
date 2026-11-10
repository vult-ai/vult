import { useEffect, useMemo, useRef, useState } from "react";
import { money, today, type Book, type State } from "../../lib/api";
import {
  BalanceRemaining,
  changeAccounts,
  type RemainingBalance,
} from "../../lib/account-changes";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { ConfirmAction, ConfirmFooter } from "../ui/confirm-action";
import { Dialog, useDialogPending } from "../ui/dialog";
import { Select } from "../ui/select";

type Position = NonNullable<State["account_balances"]>[number];
export type AccountChangeMode = "retire" | "reactivate" | "parent" | "heading";

const accountLabel = (account: Position) =>
  account.gl_code ? `${account.gl_code} · ${account.name}` : account.name;

/** What an account still holds, in each unit it is kept in. */
function holdings(account: Position, book: Book): RemainingBalance[] {
  const out: RemainingBalance[] = [];
  if (
    account.currency &&
    account.currency !== book.currency &&
    account.native_minor &&
    account.native_minor !== "0"
  )
    out.push({
      currency: account.currency,
      scale: account.scale,
      amount_minor: account.native_minor,
    });
  if (
    account.balance_minor !== "0" ||
    (account.currency === book.currency &&
      account.native_minor &&
      account.native_minor !== "0")
  )
    out.push({
      currency: book.currency,
      scale: book.scale,
      amount_minor: account.balance_minor,
    });
  return out;
}

const describe = (remaining: RemainingBalance[]) =>
  remaining
    .map((r) =>
      money(r.amount_minor.replace(/^-/, ""), {
        currency: r.currency,
        scale: r.scale,
      }),
    )
    .join(" and ");

/** Parents that keep the chart valid: the same class, active, and never the
 * account itself or one of its own descendants. */
function parentChoices(account: Position, accounts: readonly Position[]) {
  const children = new Map<string, string[]>();
  for (const a of accounts) {
    if (!a.parent_id) continue;
    const group = children.get(a.parent_id);
    if (group) group.push(a.code);
    else children.set(a.parent_id, [a.code]);
  }
  const below = new Set<string>([account.code]);
  for (const queue = [account.code]; queue.length;)
    for (const child of children.get(queue.pop()!) || [])
      if (!below.has(child)) (below.add(child), queue.push(child));
  return accounts
    .filter(
      (a) =>
        a.kind === account.kind && a.status !== "retired" && !below.has(a.code),
    )
    .sort((a, b) => accountLabel(a).localeCompare(accountLabel(b)));
}

export function AccountChangeDialogs({
  book,
  account,
  accounts,
  mode,
  returnTo,
  onClose,
}: {
  book: Book;
  account: Position;
  accounts: readonly Position[];
  mode: AccountChangeMode | null;
  returnTo?: HTMLElement | null;
  onClose: () => void;
}) {
  if (mode === "retire")
    return (
      <RetireAccountDialog
        book={book}
        account={account}
        accounts={accounts}
        returnTo={returnTo}
        onClose={onClose}
      />
    );
  if (mode === "parent")
    return (
      <ParentAccountDialog
        book={book}
        account={account}
        accounts={accounts}
        returnTo={returnTo}
        onClose={onClose}
      />
    );
  if (mode === "heading")
    return <HeadingDialog book={book} account={account} returnTo={returnTo} onClose={onClose} />;
  if (mode === "reactivate")
    return (
      <ReactivateAccountDialog
        book={book}
        account={account}
        returnTo={returnTo}
        onClose={onClose}
      />
    );
  return null;
}

function useRequestKey() {
  return useRef(crypto.randomUUID());
}

/** Retire in one step. An account that still holds money asks where it went;
 * the transfer and the retirement are recorded together, or neither. */
function RetireAccountDialog({
  book,
  account,
  accounts,
  returnTo,
  onClose,
}: {
  book: Book;
  account: Position;
  accounts: readonly Position[];
  returnTo?: HTMLElement | null;
  onClose: () => void;
}) {
  const key = useRequestKey();
  const [remaining, setRemaining] = useState(() => holdings(account, book));
  const [destination, setDestination] = useState("");
  const [date, setDate] = useState(() =>
    today(book.accounting_timezone || "UTC"),
  );
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useDialogPending(busy);
  const destinations = useMemo(
    () =>
      accounts
        .filter(
          (a) =>
            a.code !== account.code &&
            a.status !== "retired" &&
            !a.heading &&
            (!a.currency ||
              a.currency === book.currency ||
              a.currency === account.currency),
        )
        .sort((a, b) => accountLabel(a).localeCompare(accountLabel(b))),
    [accounts, account, book.currency],
  );
  const holding = remaining.length > 0;
  async function retire() {
    setBusy(true);
    setError(null);
    try {
      await changeAccounts(book.id, {
        operation: "retire_account",
        arguments: {
          request_key: key.current,
          account_id: account.code,
          expected_revision: account.revision ?? 1,
          ...(holding
            ? {
                destination_account_id: destination,
                transfer_date: date,
                ...(description.trim()
                  ? { description: description.trim() }
                  : {}),
              }
            : {}),
        },
      });
      onClose();
    } catch (e) {
      if (e instanceof BalanceRemaining) {
        // The balance changed since the page loaded: ask where it went.
        key.current = crypto.randomUUID();
        setRemaining(e.remaining);
      } else setError(e);
    } finally {
      setBusy(false);
    }
  }
  if (!holding)
    return (
      <ConfirmAction
        open
        title={`Retire ${account.name}?`}
        detail="It takes no new entries. Its history and reports stay as they are, and you can reactivate it later."
        confirmLabel="Retire account"
        busy={busy}
        error={error}
        onConfirm={() => void retire()}
        onCancel={() => !busy && onClose()}
        returnTo={returnTo}
      />
    );
  return (
    <Dialog
      open
      size="small"
      title={`Retire ${account.name}`}
      description={`This account still holds ${describe(remaining)}. Where did it go?`}
      returnTo={returnTo}
      onOpenChange={(open) => !open && !busy && onClose()}
    >
      <form
        className="dialog-form account-retire-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (destination && date && !busy) void retire();
        }}
      >
        <div className="field">
          <Select
            label="Moved to"
            hint="Vult records a transfer of the exact remaining balance to this account, then retires this one."
            value={destination}
            required
            disabled={busy}
            options={[
              { label: "Choose an account", value: "", disabled: true },
              ...destinations.map((a) => ({
                label: accountLabel(a),
                value: a.code,
              })),
            ]}
            onChange={(event) => setDestination(event.currentTarget.value)}
          />
        </div>
        <Field label="Date it moved">
          <input
            name="transfer_date"
            type="date"
            required
            value={date}
            disabled={busy}
            onChange={(event) => setDate(event.currentTarget.value)}
          />
        </Field>
        <Field
          label="What happened"
          hint="Optional, such as “Closing balance wired to checking”."
        >
          <input
            name="description"
            maxLength={4000}
            value={description}
            disabled={busy}
            onChange={(event) => setDescription(event.currentTarget.value)}
          />
        </Field>
        <ErrorMessage message={error} intent="write" />
        <ConfirmFooter
          confirmLabel="Record transfer and retire"
          tone="default"
          submit
          busy={busy}
          disabled={!destination || !date}
          onCancel={onClose}
        />
      </form>
    </Dialog>
  );
}

function ReactivateAccountDialog({
  book,
  account,
  returnTo,
  onClose,
}: {
  book: Book;
  account: Position;
  returnTo?: HTMLElement | null;
  onClose: () => void;
}) {
  const key = useRequestKey();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <ConfirmAction
      open
      tone="default"
      title={`Reactivate ${account.name}?`}
      detail="It takes new entries again. Its history is unchanged."
      confirmLabel="Reactivate account"
      busy={busy}
      error={error}
      onConfirm={async () => {
        setBusy(true);
        setError(null);
        try {
          await changeAccounts(book.id, {
            operation: "reactivate_account",
            arguments: {
              request_key: key.current,
              account_id: account.code,
              expected_revision: account.revision ?? 1,
            },
          });
          onClose();
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}
      onCancel={() => !busy && onClose()}
      returnTo={returnTo}
    />
  );
}

/** A parent groups accounts in the chart and in reports. It never moves money. */
function ParentAccountDialog({
  book,
  account,
  accounts,
  returnTo,
  onClose,
}: {
  book: Book;
  account: Position;
  accounts: readonly Position[];
  returnTo?: HTMLElement | null;
  onClose: () => void;
}) {
  const key = useRequestKey();
  const [parent, setParent] = useState(account.parent_id || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useDialogPending(busy);
  useEffect(() => {
    key.current = crypto.randomUUID();
  }, [parent, key]);
  const choices = useMemo(
    () => parentChoices(account, accounts),
    [account, accounts],
  );
  return (
    <Dialog
      open
      size="small"
      title="Parent account"
      description={`${account.name} · Reports add it into its parent. Balances don’t move.`}
      returnTo={returnTo}
      onOpenChange={(open) => !open && !busy && onClose()}
    >
      <form
        className="dialog-form"
        onSubmit={async (event) => {
          event.preventDefault();
          if (busy) return;
          setBusy(true);
          setError(null);
          try {
            await changeAccounts(book.id, {
              operation: "revise_account",
              arguments: {
                request_key: key.current,
                account_id: account.code,
                expected_revision: account.revision ?? 1,
                code: account.gl_code || "",
                name: account.name,
                parent_account_id: parent,
              },
            });
            onClose();
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="field">
          <Select
            label="Parent"
            hint="Accounts of the same type, up to four levels deep."
            value={parent}
            disabled={busy}
            options={[
              { label: "No parent", value: "" },
              ...choices.map((a) => ({
                label: accountLabel(a),
                value: a.code,
              })),
            ]}
            onChange={(event) => setParent(event.currentTarget.value)}
          />
        </div>
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button
            type="submit"
            busy={busy}
            disabled={parent === (account.parent_id || "")}
          >
            Save parent
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** A heading groups accounts and takes no new postings. Its recorded entries
 * and balance stay, and reversals can still correct them. */
function HeadingDialog({ book, account, returnTo, onClose }: {
  book: Book;
  account: Position;
  returnTo?: HTMLElement | null;
  onClose: () => void;
}) {
  const key = useRequestKey();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const making = !account.heading;
  const detail = making
    ? account.posting_count > 0
      ? `${account.name} has ${account.posting_count} recorded ${account.posting_count === 1 ? "line" : "lines"}. They stay, and so does its balance of ${money(account.balance_minor.replace(/^-/, ""), book)}, in every report. It takes no new entries: record them in the accounts under it. A reversal can still correct a line already recorded here.`
      : `${account.name} groups the accounts under it and takes no entries of its own. Reports add its accounts into it.`
    : `${account.name} takes entries of its own again, alongside the accounts under it.`;
  return (
    <ConfirmAction
      open
      tone="default"
      title={making ? `Use ${account.name} as a heading?` : `Allow postings to ${account.name}?`}
      detail={detail}
      confirmLabel={making ? "Use as a heading" : "Allow postings"}
      busy={busy}
      error={error}
      onConfirm={async () => {
        setBusy(true);
        setError(null);
        try {
          await changeAccounts(book.id, {
            operation: "revise_account",
            arguments: {
              request_key: key.current,
              account_id: account.code,
              expected_revision: account.revision ?? 1,
              code: account.gl_code || "",
              name: account.name,
              heading: making,
            },
          });
          onClose();
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}
      onCancel={() => !busy && onClose()}
      returnTo={returnTo}
    />
  );
}
