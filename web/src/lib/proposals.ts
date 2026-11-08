import { get } from "./api";
import { fetchResponse } from "./errors";
import { writeHeaders } from "./session";

/* Work an agent has prepared but not posted. It waits until the owner decides.
   Approving posts the exact proposed entry; rejecting records the owner's reason
   for the agent. */

export type ProposalStatus =
  | "approval_required"
  | "approved"
  | "rejected"
  | "expired"
  | "committed"
  | "revoked"
  | "stale";

export type ProposalSummary = {
  id: string;
  sequence: number;
  book_id: string;
  operation: "post_journal" | "reverse_journal" | string;
  sha256: string;
  created_at: string;
  /** Only proposals recorded under the earlier contract expire. */
  expires_at: string | null;
  description: string;
  economic_date: string;
  accounting_date: string;
  status: ProposalStatus;
  decision_revision: number;
};
/** waiting counts every proposal still needing a decision, whatever the page holds. */
export type ProposalPage = { version: 1; book_id: string; waiting: number; plans: ProposalSummary[]; next_before: number; has_more: boolean };

export type ProposalLine = { account_id: string; side: "debit" | "credit"; amount_minor: string; memo?: string };
export type AccountEffect = { account_id: string; class: string; before_minor: string; change_minor: string; after_minor: string };
export type Proposal = {
  id: string;
  sequence: number;
  book_id: string;
  connection_id: string;
  operation: string;
  sha256: string;
  created_at: string;
  expires_at: string | null;
  assumptions: string[] | null;
  open_questions: string[] | null;
  preview: {
    currency: string;
    scale: number;
    entries_affected: number;
    economic_date: string;
    accounting_date: string;
    description: string;
    reversal_of?: string;
    lines: ProposalLine[];
    sources: { document_id: string }[] | null;
    accounts: AccountEffect[] | null;
  };
};
export type Decision = {
  proposal_id: string;
  revision: number;
  decision: "approve" | "reject";
  reason: string;
  recorded_at: string;
  expires_at?: string;
};
export type Outcome = { status: string; operation?: string; result?: { id?: string } };
export type ProposalState = { version: 1; proposal: Proposal; status: ProposalStatus; decision?: Decision; outcome?: Outcome; balances?: AccountEffect[]; duplicate_of?: number };

const book = (id: string) => `book_id=${encodeURIComponent(id)}`;

export function readProposals(bookId: string, signal?: AbortSignal, before = 0) {
  return get<ProposalPage>(`agent-owner/plans?${book(bookId)}&limit=50${before ? `&before=${before}` : ""}`, signal);
}
/** How many proposals wait for the owner, for the sidebar badge. */
export function readWaitingCount(bookId: string, signal?: AbortSignal) {
  return get<ProposalPage>(`agent-owner/plans?${book(bookId)}&status=approval_required&limit=1`, signal).then((page) => page.waiting);
}
export function readProposal(bookId: string, id: string, signal?: AbortSignal) {
  return get<ProposalState>(`agent-owner/plans/${encodeURIComponent(id)}?${book(bookId)}`, signal);
}

/** The review link an agent hands the owner: /auth/approve?operation=… names one
 * Book and one proposal (two UUIDs, base64url). It is an address, not a permission. */
export function decodeReviewReference(operation: string | null): { bookId: string; proposalId: string } | null {
  if (!operation || !/^[A-Za-z0-9_-]{43}$/.test(operation)) return null;
  try {
    const bytes = Uint8Array.from(atob(operation.replace(/-/g, "+").replace(/_/g, "/") + "="), (c) => c.charCodeAt(0));
    if (bytes.length !== 32) return null;
    const hex = [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
    const id = (h: string) => `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`;
    return { bookId: id(hex.slice(0, 32)), proposalId: id(hex.slice(32)) };
  } catch {
    return null;
  }
}

export type DecisionFailure = { kind: "sign-in"; href?: string } | { kind: "changed" | "expired" | "failed"; message: string };
/** posted carries the entry an approval posted. */
export type DecisionResult = { ok: true; decision: Decision; posted?: { entryId?: string } } | { ok: false; failure: DecisionFailure };

/** Approve or reject exactly this proposal. Approving posts the exact proposed
 * entry. The server's specific refusals (a fresh sign-in, a changed Book, an
 * expired proposal) come back as plain outcomes; nothing is posted then. */
export async function decide(state: ProposalState, decision: "approve" | "reject", reason: string, revision: number): Promise<DecisionResult> {
  const p = state.proposal;
  let response: Response;
  try {
    response = await fetchResponse(`/api/v1/agent-owner/${decision}`, {
      method: "POST",
      headers: await writeHeaders(true),
      body: JSON.stringify({
        book_id: p.book_id,
        plan_id: p.id,
        sha256: p.sha256,
        request_key: crypto.randomUUID(),
        expected_decision_revision: revision,
        reason: reason.trim(),
      }),
    });
  } catch {
    return { ok: false, failure: { kind: "failed", message: "Vult couldn’t be reached. Check the proposal again before deciding." } };
  }
  let body: Record<string, unknown> = {};
  try { body = await response.json(); } catch { /* handled below */ }
  if (response.ok && body.decision) {
    const outcome = body.outcome as Outcome | undefined;
    return { ok: true, decision: body.decision as Decision, ...(body.status === "committed" ? { posted: { entryId: outcome?.result?.id } } : {}) };
  }
  const code = typeof body.error === "string" ? body.error : "";
  if (response.status === 403) return { ok: false, failure: { kind: "sign-in", href: typeof body.sign_in_href === "string" && body.sign_in_href.startsWith("/auth/login?") ? body.sign_in_href : undefined } };
  if (response.status === 409 && code === "proposal_expired") return { ok: false, failure: { kind: "expired", message: "This proposal expired. Ask the agent to prepare it again." } };
  if (response.status === 409 && code === "proposal_decided") return { ok: false, failure: { kind: "changed", message: "This was already posted. Check it again to see the entry." } };
  if (response.status === 409) return { ok: false, failure: { kind: "changed", message: "The Book changed since this was prepared, so nothing was posted. Ask the agent to prepare it again." } };
  return { ok: false, failure: { kind: "failed", message: "The decision wasn’t recorded and nothing was posted. Check the proposal again before deciding." } };
}

export const statusLabel: Record<ProposalStatus, string> = {
  approval_required: "Waiting for you",
  approved: "Approved",
  rejected: "Rejected",
  expired: "Expired",
  committed: "Posted",
  revoked: "Agent disconnected",
  stale: "Book changed",
};
