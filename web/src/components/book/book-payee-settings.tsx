import { useCallback, useEffect, useRef, useState } from "react";
import { User01 } from "../../design-system/icons";
import type { Book } from "../../lib/api";
import { changeAccounts, readPayees, type Payee, type PayeeKind } from "../../lib/account-changes";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { Dialog, useDialogPending } from "../ui/dialog";
import { Select } from "../ui/select";
import { SettingsRow, SettingsSection } from "../ui/settings-section";

/** The people and organizations this Book deals with. Agents link entries
 * to them from the evidence; names can change without changing who they are. */
export function BookPayeeSettings({ book }: { book: Book }) {
  const [payees, setPayees] = useState<Payee[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [editing, setEditing] = useState<Payee | "new" | null>(null);
  const trigger = useRef<HTMLElement | null>(null);
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      setPayees(await readPayees(book.id, signal));
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError(e);
    }
  }, [book.id]);
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);
  const open = (value: Payee | "new") => (event: { currentTarget: HTMLElement }) => {
    trigger.current = event.currentTarget;
    setEditing(value);
  };
  return (
    <SettingsSection
      title="Payees"
      icon={User01}
      description="Who each entry was with. Two payees with the same name stay separate."
      actions={<Button size="sm" color="secondary" onClick={open("new")}>Add payee</Button>}
    >
      {error ? (
        <ErrorMessage message={error} intent="read" />
      ) : payees === null ? (
        <SettingsRow label="Payees"><span>Loading…</span></SettingsRow>
      ) : payees.length === 0 ? (
        <SettingsRow label="Payees"><span>No payees yet</span></SettingsRow>
      ) : (
        payees.map((p) => (
          <SettingsRow
            key={p.id}
            label={p.display_name}
            description={`${p.kind === "person" ? "Person" : "Organization"} · ${p.entries} ${p.entries === 1 ? "entry" : "entries"}${p.status === "retired" ? " · Retired" : ""}`}
          >
            <Button size="sm" color="tertiary" onClick={open(p)}>Edit</Button>
          </SettingsRow>
        ))
      )}
      {editing && (
        <PayeeEditor
          book={book}
          payee={editing === "new" ? null : editing}
          returnTo={trigger.current}
          onClose={(changed) => {
            setEditing(null);
            if (changed) void load();
          }}
        />
      )}
    </SettingsSection>
  );
}

function PayeeEditor({ book, payee, returnTo, onClose }: {
  book: Book;
  payee: Payee | null;
  returnTo: HTMLElement | null;
  onClose: (changed: boolean) => void;
}) {
  const [name, setName] = useState(payee?.display_name ?? "");
  const [kind, setKind] = useState<PayeeKind>(payee?.kind ?? "organization");
  const [status, setStatus] = useState(payee?.status ?? "active");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const key = useRef(crypto.randomUUID());
  useDialogPending(busy);
  const changed = !payee || name.trim() !== payee.display_name || kind !== payee.kind || status !== payee.status;
  const ready = name.trim() !== "" && changed && (!payee || reason.trim() !== "");
  return (
    <Dialog
      open
      size="small"
      title={payee ? "Edit payee" : "Add payee"}
      description={payee ? "A new name or status is kept as a revision. Entries keep this payee." : "A person or organization this Book deals with."}
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
            await changeAccounts(
              book.id,
              payee
                ? {
                    operation: "revise_payee",
                    arguments: { request_key: key.current, payee_id: payee.id, expected_revision: payee.revision, display_name: name.trim(), kind, status, reason: reason.trim() },
                  }
                : { operation: "create_payee", arguments: { request_key: key.current, display_name: name.trim(), kind } },
            );
            onClose(true);
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
      >
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
        {payee && (
          <>
            <div className="field">
              <Select
                label="Status"
                hint="A retired payee keeps its entries but takes no new ones."
                value={status}
                disabled={busy}
                options={[
                  { label: "Active", value: "active" },
                  { label: "Retired", value: "retired" },
                ]}
                onChange={(e) => setStatus(e.currentTarget.value as "active" | "retired")}
              />
            </div>
            <Field label="Why it changes">
              <input name="reason" maxLength={4000} value={reason} disabled={busy} onChange={(e) => setReason(e.currentTarget.value)} />
            </Field>
          </>
        )}
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button type="submit" busy={busy} disabled={!ready}>
            {payee ? "Save payee" : "Add payee"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
