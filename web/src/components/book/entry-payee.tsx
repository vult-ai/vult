import { useEffect, useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { changeAccounts, readPayees, type Payee, type PayeeKind } from "../../lib/account-changes";
import type { EntryPayeeRead } from "../../lib/journal-read";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { Dialog, useDialogPending } from "../ui/dialog";
import { LoadingState } from "../ui/loading-state";
import { Select } from "../ui/select";

const label = (read: EntryPayeeRead) =>
  read.status === "payee" ? read.current?.payee_name || "Payee" : read.status === "unknown" ? "Unknown" : "Not recorded";

/** Who the entry was with. A separate decision from the entry: changing it
 * never changes the journal or the statement text it came from. */
export function EntryPayee({ book, read, onChanged }: { book: Book; read: EntryPayeeRead; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLElement | null>(null);
  return (
    <section className="entry-payee" aria-label="Payee">
      <dl className="details">
        <dt>Payee</dt>
        <dd>
          <span className={read.status === "payee" ? undefined : "text-tertiary"}>{label(read)}</span>{" "}
          <Button
            size="sm"
            color="tertiary"
            onClick={(event) => {
              trigger.current = event.currentTarget;
              setOpen(true);
            }}
          >
            {read.status === "not_recorded" ? "Set payee" : "Change"}
          </Button>
        </dd>
      </dl>
      {open && (
        <PayeeDialog
          book={book}
          read={read}
          returnTo={trigger.current}
          onClose={(changed) => {
            setOpen(false);
            if (changed) onChanged();
          }}
        />
      )}
    </section>
  );
}

const NEW = "__new";
const UNKNOWN = "__unknown";

function PayeeDialog({ book, read, returnTo, onClose }: {
  book: Book;
  read: EntryPayeeRead;
  returnTo: HTMLElement | null;
  onClose: (changed: boolean) => void;
}) {
  const [payees, setPayees] = useState<Payee[] | null>(null);
  const [choice, setChoice] = useState(read.status === "unknown" ? UNKNOWN : read.current?.payee_id || "");
  const [name, setName] = useState("");
  const [kind, setKind] = useState<PayeeKind>("organization");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const keys = useRef({ payee: crypto.randomUUID(), link: crypto.randomUUID() });
  useDialogPending(busy);
  useEffect(() => {
    const controller = new AbortController();
    readPayees(book.id, controller.signal).then(
      (list) => setPayees(list.filter((p) => p.status === "active")),
      (e) => !controller.signal.aborted && setError(e),
    );
    return () => controller.abort();
  }, [book.id]);
  const revision = read.current?.revision ?? 0;
  const unchanged = choice === (read.status === "unknown" ? UNKNOWN : read.current?.payee_id || "");
  const ready = choice !== "" && !unchanged && (choice !== NEW || name.trim() !== "") && (revision === 0 || reason.trim() !== "");
  return (
    <Dialog
      open
      size="small"
      title="Payee"
      description="Who this entry was with. The entry and its statement text stay as recorded."
      returnTo={returnTo}
      onOpenChange={(next) => !next && !busy && onClose(false)}
    >
      <form
        className="dialog-form"
        onSubmit={async (event) => {
          event.preventDefault();
          if (!ready || busy) return;
          setBusy(true);
          setError(null);
          try {
            let payee = choice === UNKNOWN ? undefined : choice;
            if (choice === NEW) {
              const created = (await changeAccounts(book.id, {
                operation: "create_payee",
                arguments: { request_key: keys.current.payee, display_name: name.trim(), kind },
              })) as Payee;
              payee = created.id;
            }
            await changeAccounts(book.id, {
              operation: "set_entry_payee",
              arguments: {
                request_key: keys.current.link,
                entry_id: read.entry_id,
                expected_revision: revision,
                ...(payee ? { payee_id: payee } : { unknown: true as const }),
                ...(revision > 0 ? { reason: reason.trim() } : {}),
              },
            });
            onClose(true);
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
      >
        {payees === null && !error ? (
          <LoadingState scope="inline" label="Loading payees…" />
        ) : (
          <div className="field">
            <Select
              label="Payee"
              hint="Choose by who it was, not by a similar name: payees with the same name are different people or organizations."
              value={choice}
              disabled={busy}
              options={[
                { label: "Choose a payee", value: "", disabled: true },
                ...(payees || []).map((p) => ({ label: `${p.display_name}${p.kind === "person" ? " · person" : ""}`, value: p.id })),
                { label: "New payee…", value: NEW },
                { label: "Unknown", value: UNKNOWN },
              ]}
              onChange={(event) => {
                setChoice(event.currentTarget.value);
                keys.current = { payee: crypto.randomUUID(), link: crypto.randomUUID() };
              }}
            />
          </div>
        )}
        {choice === NEW && (
          <>
            <Field label="Name">
              <input name="payee_name" maxLength={200} value={name} disabled={busy} onChange={(e) => setName(e.currentTarget.value)} />
            </Field>
            <div className="field">
              <Select
                label="Kind"
                value={kind}
                disabled={busy}
                options={[
                  { label: "Organization", value: "organization" },
                  { label: "Person", value: "person" },
                ]}
                onChange={(e) => setKind(e.currentTarget.value as PayeeKind)}
              />
            </div>
          </>
        )}
        {revision > 0 && (
          <Field label="Why it changes" hint="Kept with the entry's payee history.">
            <input name="reason" maxLength={4000} value={reason} disabled={busy} onChange={(e) => setReason(e.currentTarget.value)} />
          </Field>
        )}
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button type="submit" busy={busy} disabled={!ready}>
            Save payee
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
