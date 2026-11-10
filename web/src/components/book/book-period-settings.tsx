import { Lock01 } from "../../design-system/icons";
import "./book-period-settings.css";
import { useCallback, useEffect, useRef, useState } from "react";
import { bookURL, type Book } from "../../lib/api";
import { formatDate } from "../../lib/date-format";
import { fetchJSON, fetchResponse, readResponse } from "../../lib/errors";
import { writeHeaders } from "../../lib/session";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { Dialog, useDialogPending } from "../ui/dialog";
import { SettingsRow, SettingsSection } from "../ui/settings-section";

type OpenItems = { unreconciled_statements?: number; entries_without_evidence?: number; entries_without_payee?: number };
type PeriodEvent = { revision: number; event: "closed" | "reopened"; reason?: string; open_items?: OpenItems; closed_before_decisions?: boolean; recorded_at: string };
type Period = { id: string; from: string; through: string; status: "closed" | "reopened"; revision: number; events: PeriodEvent[] };
type PeriodEntry = { entry_id: string; accounting_date: string; description: string };
type Review = {
  open_items: OpenItems;
  clean: boolean;
  unreconciled_statements: { id: string; account_name?: string; period_start: string; period_end: string }[];
  entries_without_evidence: PeriodEntry[];
  entries_without_payee: PeriodEntry[];
};

const openCount = (items?: OpenItems) => (items ? Object.values(items).reduce((n, v) => n + (v ?? 0), 0) : 0);
function openWords(items: OpenItems) {
  const parts: string[] = [];
  const add = (n: number | undefined, one: string, many: string) => n && parts.push(`${n} ${n === 1 ? one : many}`);
  add(items.unreconciled_statements, "statement not reconciled", "statements not reconciled");
  add(items.entries_without_evidence, "entry with no evidence", "entries with no evidence");
  add(items.entries_without_payee, "entry with no payee", "entries with no payee");
  return parts.join(", ");
}

async function periodChange(path: "close" | "reopen", book: string, args: Record<string, unknown>) {
  const response = await fetchResponse(`/api/v1/periods/${path}`, { method: "POST", headers: await writeHeaders(), body: JSON.stringify({ book_id: book, arguments: args }) });
  if (response.status === 422) {
    const body = (await response.clone().json().catch(() => null)) as { error?: string; message?: string } | null;
    if (body?.error === "refused" && body.message) throw new Error(body.message);
  }
  return readResponse<Period>(response, "write");
}

/* A closed period accepts no entry dated inside it, from the owner or any
   agent. Closing is a recorded decision, not a permanent lock: the owner can
   reopen a period with a reason, which shows on that period's reports. */
export function BookPeriodSettings({ book }: { book: Book }) {
  const [periods, setPeriods] = useState<Period[] | null>(null);
  const [readError, setReadError] = useState<unknown>(null);
  const [from, setFrom] = useState("");
  const [through, setThrough] = useState("");
  const [closing, setClosing] = useState<HTMLElement>();
  const [reopening, setReopening] = useState<{ period: Period; trigger: HTMLElement }>();
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const out = await fetchJSON<{ periods: Period[] }>(`/api/v1/${bookURL("periods", book.id)}`, { cache: "no-store", ...(signal ? { signal } : {}) });
      setPeriods(out.periods);
      setReadError(null);
    } catch (e) {
      if (!signal?.aborted) setReadError(e);
    }
  }, [book.id]);
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);
  const valid = from !== "" && through !== "" && from <= through;
  return (
    <SettingsSection title="Closing periods" icon={Lock01} description="A closed period accepts no new entries. You can reopen it with a reason.">
      {readError ? (
        <SettingsRow label="Periods"><ErrorMessage message={readError} /></SettingsRow>
      ) : periods === null ? (
        <SettingsRow label="Periods"><span>Loading…</span></SettingsRow>
      ) : periods.length === 0 ? (
        <SettingsRow label="Periods"><span>No periods closed yet</span></SettingsRow>
      ) : (
        periods.map((p) => {
          const last = p.events[p.events.length - 1];
          return (
            <SettingsRow key={p.id} label={`${formatDate(p.from, "full")} – ${formatDate(p.through, "full")}`}
              description={p.status === "closed" ? "Closed" : "Reopened · accepts entries"}>
              <div className="book-period-row">
                <ol className="book-period-history">
                  {p.events.map((e) => (
                    <li key={e.revision} className="vult-type-metadata">
                      {e.event === "closed" ? "Closed" : "Reopened"} {formatDate(e.recorded_at.slice(0, 10), "compact")}
                      {e.closed_before_decisions ? " · no reason recorded" : e.reason ? ` · ${e.reason}` : ""}
                      {openCount(e.open_items) > 0 && <span className="text-tertiary"> · open then: {openWords(e.open_items!)}</span>}
                    </li>
                  ))}
                </ol>
                {last.event === "closed" && (
                  <Button size="sm" color="tertiary" onClick={(event) => setReopening({ period: p, trigger: event.currentTarget })}>Reopen</Button>
                )}
              </div>
            </SettingsRow>
          );
        })
      )}
      <SettingsRow label="Close a period" description="Vult shows what's still open before you close.">
        <form className="book-period-form" onSubmit={(e) => { e.preventDefault(); if (valid) setClosing(document.activeElement instanceof HTMLElement ? document.activeElement : undefined); }}>
          <Field label="From">
            <input name="from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </Field>
          <Field label="Through">
            <input name="through" type="date" value={through} min={from || undefined} onChange={(e) => setThrough(e.target.value)} />
          </Field>
          <Button type="submit" color="secondary" disabled={!valid}>Review and close</Button>
        </form>
      </SettingsRow>
      {closing && (
        <CloseDialog book={book} from={from} through={through} returnTo={closing}
          onClose={(changed) => { setClosing(undefined); if (changed) { setFrom(""); setThrough(""); void load(); } }} />
      )}
      {reopening && (
        <ReopenDialog book={book} period={reopening.period} returnTo={reopening.trigger}
          onClose={(changed) => { setReopening(undefined); if (changed) void load(); }} />
      )}
    </SettingsSection>
  );
}

function CloseDialog({ book, from, through, returnTo, onClose }: { book: Book; from: string; through: string; returnTo: HTMLElement; onClose: (changed: boolean) => void }) {
  const [review, setReview] = useState<Review>();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const key = useRef(crypto.randomUUID());
  useDialogPending(busy);
  useEffect(() => {
    const controller = new AbortController();
    fetchJSON<Review>(`/api/v1/periods/review?${new URLSearchParams({ book_id: book.id, from, through })}`, { signal: controller.signal })
      .then(setReview, (e) => !controller.signal.aborted && setError(e));
    return () => controller.abort();
  }, [book.id, from, through]);
  const ready = !!review && (review.clean || reason.trim() !== "");
  const list = (title: string, items: PeriodEntry[], total?: number) => !!total && (
    <div className="book-period-review-group">
      <p className="vult-type-ui">{title} · {total}</p>
      <ul>{items.slice(0, 5).map((e) => <li key={e.entry_id} className="vult-type-metadata">{formatDate(e.accounting_date, "compact")} · {e.description}</li>)}</ul>
      {total > 5 && <p className="vult-type-metadata text-tertiary">and {total - 5} more</p>}
    </div>
  );
  return (
    <Dialog open size="small" returnTo={returnTo} title="Close this period"
      description={`${formatDate(from, "full")} – ${formatDate(through, "full")} will accept no new entries, from you or any agent, until it's reopened.`}
      onOpenChange={(next) => !next && !busy && onClose(false)}>
      <form className="dialog-form" onSubmit={async (event) => {
        event.preventDefault();
        if (!ready || busy) return;
        setBusy(true);
        setError(undefined);
        try {
          await periodChange("close", book.id, { request_key: key.current, from, through, ...(reason.trim() ? { reason: reason.trim() } : {}) });
          onClose(true);
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}>
        {!review && !error && <p className="vult-type-metadata text-tertiary">Checking what's still open…</p>}
        {review && (review.clean ? (
          <p className="vult-type-ui">Nothing is open: every statement is reconciled and every entry has evidence and a payee.</p>
        ) : (
          <div className="book-period-review">
            <p className="vult-type-ui">Still open: {openWords(review.open_items)}.</p>
            {!!review.open_items.unreconciled_statements && (
              <div className="book-period-review-group">
                <p className="vult-type-ui">Statements not reconciled · {review.open_items.unreconciled_statements}</p>
                <ul>{review.unreconciled_statements.slice(0, 5).map((s) => <li key={s.id} className="vult-type-metadata">{s.account_name || "Account"} · {formatDate(s.period_start, "compact")} – {formatDate(s.period_end, "compact")}</li>)}</ul>
              </div>
            )}
            {list("Entries with no evidence", review.entries_without_evidence, review.open_items.entries_without_evidence)}
            {list("Entries with no payee", review.entries_without_payee, review.open_items.entries_without_payee)}
          </div>
        ))}
        {review && !review.clean && (
          <Field label="Why close it now" hint="Shown on this period's reports.">
            <input name="reason" maxLength={4000} value={reason} disabled={busy} onChange={(e) => setReason(e.currentTarget.value)} />
          </Field>
        )}
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button type="submit" busy={busy} disabled={!ready}>{review && !review.clean ? "Close with open items" : "Close period"}</Button>
        </div>
      </form>
    </Dialog>
  );
}

function ReopenDialog({ book, period, returnTo, onClose }: { book: Book; period: Period; returnTo: HTMLElement; onClose: (changed: boolean) => void }) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const key = useRef(crypto.randomUUID());
  useDialogPending(busy);
  return (
    <Dialog open size="small" returnTo={returnTo} title="Reopen this period"
      description={`${formatDate(period.from, "full")} – ${formatDate(period.through, "full")} will accept entries again. The reason shows on this period's reports.`}
      onOpenChange={(next) => !next && !busy && onClose(false)}>
      <form className="dialog-form" onSubmit={async (event) => {
        event.preventDefault();
        if (!reason.trim() || busy) return;
        setBusy(true);
        setError(undefined);
        try {
          await periodChange("reopen", book.id, { request_key: key.current, period_id: period.id, expected_revision: period.revision, reason: reason.trim() });
          onClose(true);
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}>
        <Field label="Why reopen it">
          <input name="reason" maxLength={4000} value={reason} disabled={busy} onChange={(e) => setReason(e.currentTarget.value)} />
        </Field>
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button type="submit" busy={busy} disabled={!reason.trim()}>Reopen period</Button>
        </div>
      </form>
    </Dialog>
  );
}
