import "./approvals.css";
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { CheckDone01, Stamp, ChevronDown, ChevronRight, Check, XClose, HelpCircle, Lightbulb02, Clock } from "../../design-system/icons";
import { money, type Book, type State } from "../../lib/api";
import {
  decide,
  readProposal,
  readProposals,
  statusLabel,
  type DecisionFailure,
  type ProposalState,
  type ProposalSummary,
} from "../../lib/proposals";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { ErrorMessage } from "../form";
import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";
import { Notice } from "../ui/notice";
import { RecordDate } from "../ui/record-date";
import { NativeFieldControl } from "../ui/native-field-control";
import { NativeCheckbox } from "../ui/native-checkbox";
import { formatReadableDateTime } from "../../lib/date-format";

/* Approvals, a queue the owner clears: what agents have prepared and are waiting
   on. Each row opens in place to show the exact entry, its effect on each balance,
   and the agent's questions and assumptions. Decisions wait a few seconds before
   they are sent, so a mis-click can be undone before anything is recorded.
   Pattern adapted from TUFFUI's tool approval queue (MIT). */

const UNDO_MS = 5000;
const waiting = (s: ProposalSummary) => s.status === "approval_required";
type Pending = { id: string; decision: "approve" | "reject"; reason: string; at: number };
type Outcome = { id: string; text: string; tone: "done" | "problem" };

function useNow(every = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), every);
    return () => window.clearInterval(id);
  }, [every]);
  return now;
}
/** Shown only when it matters: proposals that expire within the hour. New
 * proposals wait without a time limit; only earlier ones carry an expiry. */
function timeLeft(expires: string | null, now: number) {
  if (!expires) return null;
  const minutes = Math.ceil((Date.parse(expires) - now) / 60_000);
  if (minutes > 60) return null;
  return { text: minutes <= 0 ? "Expired" : `${minutes} min left`, urgent: minutes <= 5 };
}
const total = (s?: ProposalState) =>
  s ? s.proposal.preview.lines.filter((l) => l.side === "debit").reduce((n, l) => n + BigInt(l.amount_minor), 0n) : undefined;

export function Approvals({
  book,
  state,
  active = true,
  refreshKey,
  initialProposal,
  onWaitingChange,
}: {
  book: Book;
  state: State;
  active?: boolean;
  refreshKey?: number;
  initialProposal?: string | null;
  onWaitingChange?: (count: number) => void;
}) {
  const [rows, setRows] = useState<ProposalSummary[]>([]);
  const [details, setDetails] = useState<Record<string, ProposalState>>({});
  const detailsRef = useRef(details);
  detailsRef.current = details;
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>();
  const [open, setOpen] = useState<string | null>(initialProposal ?? null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [pending, setPending] = useState<Pending[]>([]);
  const [outcomes, setOutcomes] = useState<Outcome[]>([]);
  const [failures, setFailures] = useState<Record<string, DecisionFailure>>({});
  const [batchReason, setBatchReason] = useState("");
  const [revision, setRevision] = useState(0);
  const now = useNow();
  const names = useMemo(() => new Map((state.accounts || []).map((a) => [a.code, a.name])), [state.accounts]);

  const load = useCallback((signal?: AbortSignal) => {
    setLoading(true);
    setError(undefined);
    readProposals(book.id, signal)
      .then(async (page) => {
        if (signal?.aborted) return;
        setRows(page.plans);
        // Waiting work is read in full: the queue shows totals and can decide from a row.
        // A proposal read before is read again only when its decision has moved on.
        const known = detailsRef.current;
        const loaded = await Promise.all(page.plans.filter(waiting).slice(0, 50).map((p) => {
          const had = known[p.id];
          if (had && had.proposal.sha256 === p.sha256 && (had.decision?.revision ?? 0) === p.decision_revision) return had;
          return readProposal(book.id, p.id, signal).catch(() => null);
        }));
        if (!signal?.aborted) setDetails(Object.fromEntries(loaded.filter((d): d is ProposalState => !!d).map((d) => [d.proposal.id, d])));
      })
      .catch((e) => { if (!signal?.aborted) setError(e); })
      .finally(() => { if (!signal?.aborted) setLoading(false); });
  }, [book.id]);
  useEffect(() => {
    if (!active) return;
    const c = new AbortController();
    load(c.signal);
    return () => c.abort();
  }, [active, load, refreshKey, revision]);

  const queued = new Set(pending.map((p) => p.id));
  // Oldest first: the queue is cleared from the top.
  // Work that can no longer be approved, because its statement line was
  // recorded another way, leaves the queue for the decided list.
  const outdated = (r: ProposalSummary) => details[r.id]?.status === "stale";
  const open_ = rows.filter((r) => waiting(r) && !outdated(r) && !queued.has(r.id)).sort((a, b) => a.sequence - b.sequence);
  const decided = rows.filter((r) => !waiting(r) || outdated(r));
  useEffect(() => onWaitingChange?.(open_.length), [open_.length, onWaitingChange]);

  // Send each queued decision once its undo window has passed.
  const sending = useRef(new Set<string>());
  useEffect(() => {
    for (const p of pending) {
      if (now - p.at < UNDO_MS || sending.current.has(p.id)) continue;
      const detail = details[p.id];
      if (!detail) continue;
      sending.current.add(p.id);
      void decide(detail, p.decision, p.reason, detail.decision?.revision ?? 0).then((result) => {
        sending.current.delete(p.id);
        setPending((list) => list.filter((x) => x.id !== p.id));
        if (result.ok) {
          setOutcomes((list) => [{ id: p.id, tone: "done" as const, text: p.decision === "reject" ? "Rejected" : result.posted ? "Approved and posted" : "Approved" }, ...list].slice(0, 20));
          setRevision((n) => n + 1);
        } else {
          setFailures((f) => ({ ...f, [p.id]: result.failure }));
          setOpen(p.id);
        }
      });
    }
  }, [now, pending, details]);

  // A decision already made is sent, not dropped, when the queue goes away during its
  // undo window (another Book, a format change). Closing the tab asks first.
  const latest = useRef({ pending, details });
  latest.current = { pending, details };
  useEffect(() => () => {
    const { pending: left, details: known } = latest.current;
    for (const p of left) {
      const detail = known[p.id];
      if (detail && !sending.current.has(p.id)) void decide(detail, p.decision, p.reason, detail.decision?.revision ?? 0);
    }
  }, []);
  useEffect(() => {
    if (!pending.length) return;
    const hold = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", hold);
    return () => window.removeEventListener("beforeunload", hold);
  }, [pending.length]);

  const queue = (ids: string[], decision: "approve" | "reject", reason: string) => {
    setPending((list) => [...list.filter((p) => !ids.includes(p.id)), ...ids.map((id) => ({ id, decision, reason, at: Date.now() }))]);
    setFailures((f) => Object.fromEntries(Object.entries(f).filter(([k]) => !ids.includes(k))));
    setPicked(new Set());
    if (open && ids.includes(open)) setOpen(null);
  };
  const undo = (id: string) => setPending((list) => list.filter((p) => p.id !== id));

  // Arrow keys move between rows, Space selects, Enter opens.
  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    const rowsEls = [...e.currentTarget.querySelectorAll<HTMLElement>("[data-approval-row]")];
    const i = rowsEls.indexOf(document.activeElement as HTMLElement);
    if (i < 0) return;
    const id = rowsEls[i].dataset.approvalRow!;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      rowsEls[Math.max(0, Math.min(rowsEls.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)))]?.focus();
    } else if (e.key === " ") {
      e.preventDefault();
      setPicked((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });
    } else if (e.key === "Enter") {
      e.preventDefault();
      setOpen((o) => (o === id ? null : id));
    }
  };

  const selected = [...picked].filter((id) => open_.some((r) => r.id === id) && details[id]);
  return (
    <section className="approvals" aria-label="Approvals">
      <header className="approvals-header">
        <div className="approvals-title">
          <Stamp className="size-5" aria-hidden />
          <h2 className="vult-type-label">Approvals</h2>
          <span className="vult-type-metadata text-tertiary">{open_.length ? `${open_.length} waiting` : "Nothing waiting"}</span>
        </div>
        {selected.length > 0 && (
          <div className="approvals-batch" role="group" aria-label="Decide selected">
            <span className="vult-type-metadata text-secondary">{selected.length} selected</span>
            <span className="approvals-batch-reason">
              <NativeFieldControl>
                <input aria-label="Reason for the selected" placeholder="Reason" value={batchReason} onChange={(e) => setBatchReason(e.target.value)} />
              </NativeFieldControl>
            </span>
            <Button color="secondary" size="sm" isDisabled={!batchReason.trim()} onPress={() => { queue(selected, "reject", batchReason); setBatchReason(""); }}>Reject</Button>
            <Button color="primary" size="sm" onPress={() => { queue(selected, "approve", batchReason.trim() || "Approved"); setBatchReason(""); }}>Approve {selected.length}</Button>
          </div>
        )}
      </header>
      <ErrorMessage message={error} intent="read" resource="approvals" pending={loading} onRetry={() => load()} />

      {loading && !rows.length ? <LoadingState scope="region" label="Loading approvals…" />
        : !error && !open_.length && !pending.length ? (
          <EmptyState scope="region" icon={CheckDone01} title="All clear" detail="When an agent prepares an entry that needs approval, it waits here until you decide." />
        ) : (
          <>
            {open_.length > 0 && <h2 className="approvals-group vult-type-metadata text-tertiary">Waiting for you</h2>}
            <ul className="approvals-list" onKeyDown={onKeyDown}>
              {open_.map((r) => {
                const d = details[r.id];
                const isOpen = open === r.id;
                const left = timeLeft(r.expires_at, now);
                const sum = total(d);
                return (
                  <li key={r.id} className="approval-card" data-open={isOpen || undefined}>
                    <div className="approval-row" data-approval-row={r.id} tabIndex={0} role="button" aria-expanded={isOpen}
                      aria-label={`${r.description || "Proposed entry"}${sum !== undefined ? `, ${money(sum.toString(), book)}` : ""}`}
                      onClick={(e) => { if ((e.target as HTMLElement).closest("button,input")) return; setOpen(isOpen ? null : r.id); }}>
                      <NativeCheckbox aria-label={`Select ${r.description}`} checked={picked.has(r.id)}
                        onChange={() => setPicked((s) => { const n = new Set(s); n.has(r.id) ? n.delete(r.id) : n.add(r.id); return n; })} />
                      {isOpen ? <ChevronDown className="size-4 text-tertiary" aria-hidden /> : <ChevronRight className="size-4 text-tertiary" aria-hidden />}
                      <span className="approval-row-text">
                        <span className="approval-title vult-type-label text-primary">{r.description || "Proposed entry"}</span>
                        {sum !== undefined && <span className="approval-sum vult-type-ui text-primary">{money(sum.toString(), book)}</span>}
                        <span className="approval-meta vult-type-metadata text-tertiary">
                          <RecordDate value={r.accounting_date} /> · Prepared {formatReadableDateTime(r.created_at)}
                        </span>
                        {d?.duplicate_of ? <span className="approval-duplicate vult-type-metadata">Duplicate of a newer copy below</span> : null}
                      </span>
                      {left && <span className="approval-time vult-type-metadata" data-urgent={left.urgent || undefined}><Clock className="size-3.5" aria-hidden />{left.text}</span>}
                      {!isOpen && d?.duplicate_of ? (
                        <span className="approval-quick">
                          <Button color="secondary" size="sm" onPress={() => queue([r.id], "reject", "Duplicate of a newer copy")}>Reject duplicate</Button>
                        </span>
                      ) : !isOpen && d && (
                        <span className="approval-quick">
                          <Button color="tertiary" size="sm" aria-label={`Reject ${r.description}`} iconLeading={XClose} onPress={() => setOpen(r.id)} />
                          <Button color="secondary" size="sm" aria-label={`Approve ${r.description}`} iconLeading={Check} onPress={() => queue([r.id], "approve", "Approved")} />
                        </span>
                      )}
                    </div>
                    {isOpen && (d
                      ? <ProposalDetail state={d} names={names} failure={failures[r.id]} onDecide={(decision, reason) => queue([r.id], decision, reason)} />
                      : <LoadingState scope="inline" label="Loading proposal…" />)}
                  </li>
                );
              })}
            </ul>
          </>
        )}

      {(pending.length > 0 || outcomes.length > 0 || decided.length > 0) && (
        <>
          <h2 className="approvals-group vult-type-metadata text-tertiary">Decided</h2>
          <ul className="approvals-log">
            {pending.map((p) => {
              const r = rows.find((x) => x.id === p.id);
              const seconds = Math.max(0, Math.ceil((UNDO_MS - (now - p.at)) / 1000));
              return (
                <li key={`p-${p.id}`}>
                  {p.decision === "approve" ? <Check className="size-4 approval-ok" aria-hidden /> : <XClose className="size-4 text-tertiary" aria-hidden />}
                  <span className="vult-type-ui">{r?.description || "Proposed entry"}</span>
                  <span className="vult-type-metadata text-tertiary">{p.decision === "approve" ? "Approving" : "Rejecting"}{seconds ? ` in ${seconds}s` : "…"}</span>
                  {seconds > 0 && <Button color="secondary" size="sm" onPress={() => undo(p.id)}>Undo</Button>}
                </li>
              );
            })}
            {decided.map((r) => (
              <li key={r.id}>
                {r.status === "committed" || r.status === "approved" ? <Check className="size-4 approval-ok" aria-hidden /> : <XClose className="size-4 text-tertiary" aria-hidden />}
                <span className="vult-type-ui text-secondary">{r.description || "Proposed entry"}</span>
                <span className="vult-type-metadata text-tertiary">
                  {outdated(r) ? "Out of date, nothing to do" : r.status === "approved" ? "Approved, waiting for the agent to post it" : statusLabel[r.status] ?? r.status}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

const accountChangeLabel: Record<string, string> = { create: "New account", revise: "Revised", retire: "Retired", reactivate: "Reactivated" };

function ProposalDetail({ state, names, failure, onDecide }: {
  state: ProposalState;
  names: Map<string, string>;
  failure?: DecisionFailure;
  onDecide: (decision: "approve" | "reject", reason: string) => void;
}) {
  const [reason, setReason] = useState("");
  const [needReason, setNeedReason] = useState(false);
  const p = state.proposal;
  const v = p.preview;
  const amount = (minor: string) => money(minor, { currency: v.currency, scale: v.scale });
  const name = (id: string) => names.get(id) || `Account ${id.slice(0, 8)}`;
  // Balances as they are now; other entries may have moved them since this was prepared.
  const after = new Map((state.balances ?? v.accounts ?? []).map((a) => [a.account_id, a]));
  return (
    <div className="approval-detail">
      {p.operation === "reverse_journal" && <p className="vult-type-metadata text-secondary">Reverses an existing entry in full.</p>}
      {!!v.account_changes?.length && (
        <table className="approval-lines approval-account-changes vult-type-ui">
          <caption className="sr-only">Changes to the chart of accounts</caption>
          <thead className="vult-type-metadata"><tr><th scope="col">Account</th><th scope="col">Change</th><th scope="col">Parent</th></tr></thead>
          <tbody>
            {v.account_changes.map((c, i) => (
              <tr key={i}>
                <th scope="row">{c.code ? `${c.code} · ${c.name}` : c.name}</th>
                <td>{accountChangeLabel[c.change]}{c.heading ? " · heading" : ""}</td>
                <td className="text-tertiary">{c.parent_account_id ? name(c.parent_account_id) : c.parent_code || "None"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {v.payee_change && (
        <p className="vult-type-ui">
          {v.payee_change.change === "link"
            ? v.payee_change.counterpart === "payee"
              ? `Records ${v.payee_change.display_name} as who this entry was with.`
              : "Records who this entry was with as unknown."
            : `${v.payee_change.change === "create" ? "Adds" : "Revises"} the payee ${v.payee_change.display_name}${v.payee_change.status === "retired" ? ", retired" : ""}.`}
        </p>
      )}
      {v.statement_change && (
        <p className="vult-type-ui">
          {{
            import: `Imports the statement for ${v.statement_change.period_start} to ${v.statement_change.period_end}, ${v.statement_change.rows ?? 0} rows. It posts nothing.`,
            match: `Records that ${v.statement_change.row_ids?.length ?? 0} statement ${v.statement_change.row_ids?.length === 1 ? "row is" : "rows are"} the same money as ${v.statement_change.line_ids?.length ?? 0} posted ${v.statement_change.line_ids?.length === 1 ? "line" : "lines"}${v.statement_change.amount_minor ? `, ${amount(v.statement_change.amount_minor)}` : ""}.`,
            revoke: `Undoes a statement match${v.statement_change.reason ? `: ${v.statement_change.reason}` : "."}`,
            adjust: "Links a statement row to the entry posted for it.",
            duplicate: `Marks a statement row as repeating another${v.statement_change.reason ? `: ${v.statement_change.reason}` : "."}`,
            review: `Records the statement for ${v.statement_change.period_start} to ${v.statement_change.period_end} as ${v.statement_change.outcome === "reconciled" ? "reconciled" : "needing work"}${v.statement_change.reason ? `: ${v.statement_change.reason}` : "."}`,
          }[v.statement_change.change]}
        </p>
      )}
      {v.period_change && (
        <p className="vult-type-ui">
          {v.period_change.change === "reopen"
            ? `Reopens ${v.period_change.from} to ${v.period_change.through} so it accepts entries again: ${v.period_change.reason}`
            : `Closes ${v.period_change.from} to ${v.period_change.through} so it accepts no new entries.${Object.values(v.period_change.open_items ?? {}).some((n) => n > 0) ? ` Still open: ${Object.entries(v.period_change.open_items ?? {}).filter(([, n]) => n > 0).map(([k, n]) => `${n} ${k.replaceAll("_", " ")}`).join(", ")}. Reason: ${v.period_change.reason || "none given"}` : ""}`}
        </p>
      )}
      {v.entries_affected > 0 && v.account_changes?.some((c) => c.change === "retire") && (
        <p className="vult-type-metadata text-secondary">The remaining balance moves in this entry, recorded with the retirement.</p>
      )}
      {((!v.account_changes && !v.payee_change && !v.statement_change && !v.period_change) || v.lines.length > 0) && <table className="approval-lines vult-type-ui">
        <caption className="sr-only">Lines of the proposed entry and the balance after each</caption>
        <thead className="vult-type-metadata"><tr><th scope="col">Account</th><th scope="col">Debit</th><th scope="col">Credit</th><th scope="col">Balance after</th></tr></thead>
        <tbody>
          {v.lines.map((l, i) => {
            const a = after.get(l.account_id);
            return (
              <tr key={i}>
                <th scope="row">{name(l.account_id)}{l.memo && <small className="vult-type-metadata text-tertiary">{l.memo}</small>}</th>
                <td>{l.side === "debit" ? amount(l.amount_minor) : ""}</td>
                <td>{l.side === "credit" ? amount(l.amount_minor) : ""}</td>
                <td className="text-tertiary">{a ? <>{amount(a.before_minor)} → <b className="text-primary">{amount(a.after_minor)}</b></> : ""}</td>
              </tr>
            );
          })}
        </tbody>
      </table>}
      {!!p.open_questions?.length && (
        <div className="approval-question vult-type-ui">
          <HelpCircle className="size-4" aria-hidden />
          <div><span className="sr-only">The agent asks: </span>{p.open_questions.map((q, i) => <p key={i}>{q}</p>)}</div>
        </div>
      )}
      {!!p.assumptions?.length && (
        <div className="approval-assumed vult-type-metadata text-secondary">
          <Lightbulb02 className="size-4" aria-hidden />
          <div>Assumed: {p.assumptions.join(" ")}</div>
        </div>
      )}
      <p className="vult-type-metadata text-tertiary">{v.sources?.length ? `${v.sources.length} source ${v.sources.length === 1 ? "document" : "documents"} attached` : "No source documents"}</p>
      {failure?.kind === "sign-in" ? (
        <Notice tone="caution" action={failure.href ? <Button color="secondary" size="sm" href={failure.href}>Sign in</Button> : undefined}>
          Sign in again to confirm it’s you, then decide. Signing in never approves anything by itself.
        </Notice>
      ) : failure && <Notice tone="caution">{failure.message}</Notice>}
      <div className="approval-decide">
        <span className="approval-reason">
          <NativeFieldControl>
            <input aria-label="Reason" placeholder={needReason ? "What should change?" : "Reason (the agent sees it)"} value={reason}
              aria-invalid={needReason && !reason.trim() ? true : undefined} onChange={(e) => { setReason(e.target.value); setNeedReason(false); }} />
          </NativeFieldControl>
        </span>
        <Button color="secondary" size="sm" onPress={() => (reason.trim() ? onDecide("reject", reason) : setNeedReason(true))}>Reject</Button>
        <Button color="primary" size="sm" onPress={() => onDecide("approve", reason.trim() || "Approved")}>Approve</Button>
      </div>
      {needReason && <p className="vult-type-metadata approval-need" role="alert">Say what should change, so the agent can prepare it again.</p>}
    </div>
  );
}
