import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeft, BookOpen01, CheckDone01, File06, Lightbulb02, Scales02 } from "../../design-system/icons";
import { money, type Book } from "../../lib/api";
import { changeAccounts, ChangeRefused, type StatementChange } from "../../lib/account-changes";
import { formatDate, formatDateRange } from "../../lib/date-format";
import {
  readReconciliation, readStatements, rowStateLabel, statementStatusLabel,
  type Reconciliation, type Statement, type StatementSuggestion,
} from "../../lib/statements";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { Dialog, useDialogPending } from "../ui/dialog";
import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";
import { Notice } from "../ui/notice";
import { DashboardPanel } from "../v0/dashboard-panels";
import "./reconcile.css";

type Props = { book: Book; active: boolean; refreshKey?: number; onInspect: (id: string, trigger: HTMLElement) => void };

const statementParam = "statement";
const readSelected = () => new URLSearchParams(window.location.search).get(statementParam) || undefined;
function select(id?: string) {
  const url = new URL(window.location.href);
  if (id) url.searchParams.set(statementParam, id);
  else url.searchParams.delete(statementParam);
  window.history.pushState(window.history.state, "", url);
}

/** Bank and card statements compared with the Book. Agents import them from
 * Sources; the owner and write agents match, explain and sign them off. */
export function Reconcile({ book, active, refreshKey, onInspect }: Props) {
  const [statements, setStatements] = useState<Statement[]>();
  const [failure, setFailure] = useState<unknown>();
  const [selected, setSelected] = useState(readSelected);
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const pop = () => setSelected(readSelected());
    window.addEventListener("popstate", pop);
    return () => window.removeEventListener("popstate", pop);
  }, []);
  useEffect(() => {
    if (!active || selected) return;
    const controller = new AbortController();
    setFailure(undefined);
    readStatements(book.id, controller.signal).then((value) => !controller.signal.aborted && setStatements(value), (e) => !controller.signal.aborted && setFailure(e));
    return () => controller.abort();
  }, [book.id, active, refreshKey, reload, selected]);
  const open = (id?: string) => { select(id); setSelected(id); };
  if (selected)
    return <StatementDetail key={selected} book={book} statementId={selected} active={active} refreshKey={refreshKey} onBack={() => open(undefined)} onInspect={onInspect} />;
  const groups = new Map<string, Statement[]>();
  for (const s of statements ?? []) {
    const group = groups.get(s.account_id);
    if (group) group.push(s);
    else groups.set(s.account_id, [s]);
  }
  return (
    <section className="reconcile dash-surface" aria-label="Reconcile">
      <DashboardPanel icon={Scales02} title="Reconcile" subtitle="Bank and card statements, compared with the Book">
        <ErrorMessage message={failure} intent="read" resource="statement" onRetry={() => setReload((n) => n + 1)} />
        {!statements && !failure ? <LoadingState scope="region" label="Loading statements…" />
          : statements && statements.length === 0 ? (
            <EmptyState scope="region" icon={CheckDone01} title="No statements yet" detail="Add a bank or card statement in Sources. An agent imports it here and matches it to the Book." />
          ) : [...groups.values()].map((list) => (
            <section key={list[0].account_id} className="reconcile-account" aria-label={list[0].account_name || "Account"}>
              <h3 className="vult-type-metadata">{list[0].account_name || "Account"}<span className="text-tertiary"> · {list[0].kind === "card" ? "Card" : "Bank"} · {list[0].currency}</span></h3>
              <ul className="reconcile-list">
                {list.map((s) => (
                  <li key={s.id}>
                    <button type="button" className="reconcile-statement vult-focus-target" onClick={() => open(s.id)}>
                      <span className="reconcile-statement-period vult-type-ui">{formatDateRange(s.period_start, s.period_end, "compact")}</span>
                      <span className="reconcile-statement-closing vult-money vult-type-ui">{money(s.closing_balance_minor, s)}</span>
                      <span className="reconcile-status" data-status={s.status}>{statementStatusLabel[s.status]}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          ))}
      </DashboardPanel>
    </section>
  );
}

function StatementDetail({ book, statementId, active, refreshKey, onBack, onInspect }: {
  book: Book; statementId: string; active: boolean; refreshKey?: number; onBack: () => void; onInspect: (id: string, trigger: HTMLElement) => void;
}) {
  const [data, setData] = useState<{ reconciliation: Reconciliation; suggestions: StatementSuggestion[] }>();
  const [failure, setFailure] = useState<unknown>();
  const [pending, setPending] = useState(true);
  const [busy, setBusy] = useState<string>();
  const [actionError, setActionError] = useState<unknown>();
  const [review, setReview] = useState<{ outcome: "reconciled" | "needs_work"; trigger: HTMLElement }>();
  const [reload, setReload] = useState(0);
  useEffect(() => {
    setReview(undefined);
    if (!active) return;
    const controller = new AbortController();
    setPending(true);
    readReconciliation(book.id, statementId, controller.signal).then((value) => {
      if (!controller.signal.aborted) { setData(value); setFailure(undefined); setPending(false); }
    }, (e) => {
      if (!controller.signal.aborted) { setFailure(e); setPending(false); }
    });
    return () => controller.abort();
  }, [book.id, statementId, active, refreshKey, reload]);
  const refresh = () => { setPending(true); setReload((n) => n + 1); };
  const current = active && !pending && !failure;
  const run = async (id: string, change: StatementChange) => {
    if (!current || busy) return;
    setBusy(id);
    setActionError(undefined);
    try {
      await changeAccounts(book.id, change);
      refresh();
    } catch (e) {
      setActionError(e instanceof ChangeRefused ? e.message : e);
    } finally {
      setBusy(undefined);
    }
  };
  const r = data?.reconciliation;
  const st = r?.statement;
  const m = (minor: string) => (st ? money(minor, st) : "");
  const rowsByID = useMemo(() => new Map((r?.rows ?? []).map((x) => [x.id, x])), [r]);
  const lines = useMemo(() => new Map((r?.outstanding ?? []).map((x) => [x.line_id, x])), [r]);
  if (!r || !st)
    return (
      <section className="reconcile dash-surface" aria-label="Statement">
        <Button size="sm" color="tertiary" onClick={onBack}><ArrowLeft aria-hidden />All statements</Button>
        <ErrorMessage message={failure} intent="read" resource="statement" pending={pending} onRetry={refresh} />
        {pending && !failure && <LoadingState scope="region" label="Comparing the statement with the Book…" />}
      </section>
    );
  const s = r.summary;
  const key = () => crypto.randomUUID();
  return (
    <section className="reconcile dash-surface" aria-label={`${st.account_name || "Account"} statement`} aria-busy={pending}>
      <Button size="sm" color="tertiary" className="reconcile-back" onClick={onBack}><ArrowLeft aria-hidden />All statements</Button>
      <DashboardPanel icon={Scales02} title={st.account_name || "Account"}
        subtitle={<>{formatDateRange(st.period_start, st.period_end, "compact")} · {st.kind === "card" ? "Card" : "Bank"} statement · {st.currency}</>}
        action={<span className="reconcile-status" data-status={st.status}>{!current && "Last loaded · "}{statementStatusLabel[st.status]}</span>}>
        {st.review?.reason && <p className="vult-type-metadata text-tertiary">Last review: {statementStatusLabel[st.review.outcome]}, {formatDate(st.review.recorded_at.slice(0, 10), "compact")} — {st.review.reason}</p>}
        <ErrorMessage message={failure} intent="read" resource="statement" retained="view" pending={pending} onRetry={refresh} />
        {pending && !failure && <LoadingState scope="inline" label="Refreshing the comparison…" />}
        {!current && <p className="vult-type-metadata text-tertiary">These are the last loaded figures. Changes are unavailable until the comparison refreshes.</p>}
        {current && (s.fully_explained
          ? <Notice tone="success">{`Every settled row on this statement is explained. Opening ${m(s.opening_balance_minor)} plus the matched, adjusted and repeated rows equals the closing ${m(s.closing_balance_minor)}.`}</Notice>
          : <Notice tone="info">{`${s.on_statement_not_in_book_count} ${s.on_statement_not_in_book_count === 1 ? "row isn't" : "rows aren't"} in the Book yet, a difference of ${m(s.remaining_difference_minor)}.${st.evidence_state !== "complete" ? ` The statement is marked ${st.evidence_state}.` : ""}`}</Notice>)}
        <ErrorMessage message={actionError} intent="write" />
        <dl className="reconcile-summary">
          <div><dt>Opening</dt><dd className="vult-money">{m(s.opening_balance_minor)}</dd></div>
          <div><dt>Matched</dt><dd className="vult-money">{m(s.matched_minor)}</dd></div>
          <div><dt>Adjusted</dt><dd className="vult-money">{m(s.adjusted_minor)}</dd></div>
          {s.duplicates > 0 && <div><dt>Repeated from another statement</dt><dd className="vult-money">{m(s.duplicate_minor)}<span className="text-tertiary"> · {s.duplicates}</span></dd></div>}
          <div><dt>On the statement, not in the Book</dt><dd className="vult-money">{m(s.on_statement_not_in_book_minor)}<span className="text-tertiary"> · {s.on_statement_not_in_book_count}</span></dd></div>
          <div><dt>Closing</dt><dd className="vult-money">{m(s.closing_balance_minor)}</dd></div>
          <div><dt>Remaining difference</dt><dd className="vult-money">{m(s.remaining_difference_minor)}</dd></div>
          <div><dt>In the Book, not on a statement yet</dt><dd className="vult-money">{s.unresolved_lines > 0 ? "—" : m(s.outstanding_in_book_minor)}<span className="text-tertiary">{s.unresolved_lines > 0 ? ` · ${s.unresolved_lines} unresolved` : ` · ${s.outstanding_in_book_count}`}</span></dd></div>
          {s.pending_rows > 0 && <div><dt>Not counted</dt><dd>{s.pending_rows} pending or cancelled</dd></div>}
        </dl>
        {s.statement_arithmetic_difference_minor !== "0" && <Notice tone="caution">{`The statement's rows don't add up to its closing balance (off by ${m(s.statement_arithmetic_difference_minor)}). Some rows may be missing from the import.`}</Notice>}
        <div className="reconcile-actions">
          <Button size="sm" color="secondary" disabled={!current || !!busy} onClick={(e) => setReview({ outcome: "needs_work", trigger: e.currentTarget })}>Needs work</Button>
          <Button size="sm" color="primary" disabled={!current || !!busy || !s.fully_explained} title={s.fully_explained ? undefined : "Every settled row must be matched, adjusted or explained by a repeated row first."}
            onClick={(e) => setReview({ outcome: "reconciled", trigger: e.currentTarget })}>Mark reconciled</Button>
        </div>
      </DashboardPanel>
      {data.suggestions.length > 0 && (
        <DashboardPanel icon={Lightbulb02} title="Suggested" subtitle="Nothing is recorded until you accept">
          <ul className="reconcile-suggestions">
            {data.suggestions.map((x) => {
              const row = rowsByID.get(x.row_id);
              const line = x.line_ids?.[0] ? lines.get(x.line_ids[0]) : undefined;
              const id = `${x.kind}:${x.row_id}`;
              return (
                <li key={id}>
                  <span className="reconcile-suggestion-text vult-type-ui">
                    {x.kind === "match"
                      ? <>Row {row?.row_number} · {row?.description} <span className="text-tertiary">matches</span> {line?.description} <span className="text-tertiary">({line && formatDate(line.date, "compact")})</span></>
                      : <>Row {row?.row_number} · {row?.description} <span className="text-tertiary">repeats an earlier statement</span></>}
                    <span className="text-tertiary"> · {x.reason}</span>
                  </span>
                  <span className="vult-money vult-type-ui">{m(x.amount_minor)}</span>
                  <Button size="sm" color="secondary" busy={busy === id} disabled={!current || !!busy}
                    onClick={() => run(id, x.kind === "match"
                      ? { operation: "match_statement", arguments: { request_key: key(), rows: [{ row_id: x.row_id }], lines: [{ line_id: x.line_ids![0] }], reason: "Accepted the suggested match" } }
                      : { operation: "mark_statement_duplicate", arguments: { request_key: key(), row_id: x.row_id, canonical_row_id: x.canonical_row_id!, reason: "The same row appears on an earlier statement" } })}>
                    {x.kind === "match" ? "Match" : "Mark duplicate"}
                  </Button>
                </li>
              );
            })}
          </ul>
        </DashboardPanel>
      )}
      <DashboardPanel icon={File06} title="Statement rows" subtitle={`${r.rows.length} ${r.rows.length === 1 ? "row" : "rows"} on the statement`} className="reconcile-table-panel">
        <div className="reconcile-table-frame" tabIndex={0} role="region" aria-label="Statement rows, scrolls sideways">
          <table className="reconcile-table">
            <thead><tr><th scope="col">#</th><th scope="col">Date</th><th scope="col">Description</th><th scope="col" className="num">Amount</th><th scope="col">State</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {r.rows.map((x) => (
                <tr key={x.id} data-state={x.state}>
                  <td>{x.row_number}</td>
                  <td>{formatDate(x.date, "compact")}</td>
                  <td className="reconcile-desc">{x.description}</td>
                  <td className="num vult-money">{m(x.amount_minor)}</td>
                  <td><span className="reconcile-row-state" data-state={x.state}>{rowStateLabel[x.state]}</span></td>
                  <td>
                    {x.state === "matched" && x.match_ids.length > 0 && (
                      <Button size="sm" color="tertiary" busy={busy === `undo:${x.id}`} disabled={!current || !!busy}
                        onClick={() => run(`undo:${x.id}`, { operation: "revoke_statement_match", arguments: { request_key: key(), match_id: x.match_ids[0], reason: "The owner undid this match" } })}>Undo match</Button>
                    )}
                    {x.state === "adjusted" && x.adjustment_entry_id && (
                      <Button size="sm" color="tertiary" onClick={(e) => onInspect(x.adjustment_entry_id!, e.currentTarget)}>Open entry</Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </DashboardPanel>
      <DashboardPanel icon={BookOpen01} title="In the Book, not on a statement yet" subtitle={r.outstanding.length === 1 ? "1 entry" : `${r.outstanding.length} entries`} className="reconcile-table-panel">
        {r.outstanding.length === 0 ? <p className="vult-type-metadata text-tertiary">Nothing outstanding.</p> : (
          <div className="reconcile-table-frame" tabIndex={0} role="region" aria-label="Outstanding entries, scrolls sideways">
            <table className="reconcile-table">
              <thead><tr><th scope="col">Date</th><th scope="col">Entry</th><th scope="col" className="num">Amount</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
              <tbody>
                {r.outstanding.map((o) => (
                  <tr key={o.line_id}>
                    <td>{formatDate(o.date, "compact")}</td>
                    <td className="reconcile-desc">{o.description}{o.unresolved && <span className="text-tertiary">{o.amount_minor !== undefined ? " · clearing allocation at this cutoff is unresolved" : ` · no amount in ${st.currency}`}</span>}{o.proposed && <span className="text-tertiary"> · proposed</span>}</td>
                    <td className="num vult-money">{o.remaining_minor ? m(o.remaining_minor) : "—"}</td>
                    <td><Button size="sm" color="tertiary" onClick={(e) => onInspect(o.entry_id, e.currentTarget)}>Open entry</Button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </DashboardPanel>
      {review && current && (
        <ReviewDialog statement={st} outcome={review.outcome} returnTo={review.trigger} book={book}
          onClose={(changed) => { setReview(undefined); if (changed) refresh(); }} />
      )}
    </section>
  );
}

function ReviewDialog({ book, statement, outcome, returnTo, onClose }: {
  book: Book; statement: Statement; outcome: "reconciled" | "needs_work"; returnTo: HTMLElement; onClose: (changed: boolean) => void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const key = useRef(crypto.randomUUID());
  useDialogPending(busy);
  const ready = outcome === "reconciled" || reason.trim() !== "";
  return (
    <Dialog open size="small" returnTo={returnTo}
      title={outcome === "reconciled" ? "Mark this statement reconciled" : "Mark this statement as needing work"}
      description={outcome === "reconciled"
        ? "Vult keeps the exact comparison you're signing off. You can review it again later."
        : "Say what's left to do. The statement stays open for the agent."}
      onOpenChange={(next) => !next && !busy && onClose(false)}>
      <form className="dialog-form" onSubmit={async (event) => {
        event.preventDefault();
        if (!ready || busy) return;
        setBusy(true);
        setError(undefined);
        try {
          await changeAccounts(book.id, { operation: "review_statement", arguments: { request_key: key.current, statement_id: statement.id, expected_revision: statement.review?.revision ?? 0, outcome, ...(reason.trim() ? { reason: reason.trim() } : {}) } });
          onClose(true);
        } catch (e) {
          setError(e instanceof ChangeRefused ? e.message : e);
        } finally {
          setBusy(false);
        }
      }}>
        <Field label={outcome === "reconciled" ? "Note (optional)" : "What's left to do"}>
          <input name="reason" maxLength={4000} value={reason} disabled={busy} onChange={(e) => setReason(e.currentTarget.value)} />
        </Field>
        <ErrorMessage message={error} intent="write" />
        <div className="dialog-footer">
          <Button type="submit" busy={busy} disabled={!ready}>{outcome === "reconciled" ? "Mark reconciled" : "Save"}</Button>
        </div>
      </form>
    </Dialog>
  );
}
