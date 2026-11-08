import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Synthetic Book", currency: "USD", scale: 2 };
const state = { book, accounts: [{ code: "acct-cash", name: "Checking", kind: "asset" }, { code: "acct-sales", name: "Sales", kind: "income" }] };
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const settle = () => React.act(() => new Promise((r) => setTimeout(r, 10)));
const later = (minutes) => new Date(Date.now() + minutes * 60000).toISOString();
const summary = (id, status, sequence, description) => ({ id, sequence, book_id: book.id, operation: "post_journal", sha256: "a".repeat(64), created_at: later(-5), expires_at: later(10), description, economic_date: "2026-11-04", accounting_date: "2026-11-04", status, decision_revision: 0, dependency_status: "not_checked" });
const detail = (id, status) => ({
  version: 1, status,
  proposal: {
    id, sequence: 2, book_id: book.id, connection_id: "conn-1", operation: "post_journal", sha256: "a".repeat(64),
    created_at: later(-5), expires_at: later(10), assumptions: ["The deposit is the November retainer."], open_questions: ["Is this a project payment?"],
    preview: { currency: "USD", scale: 2, entries_affected: 1, economic_date: "2026-11-04", accounting_date: "2026-11-04", description: "Client retainer",
      lines: [{ account_id: "acct-cash", side: "debit", amount_minor: "225000" }, { account_id: "acct-sales", side: "credit", amount_minor: "225000" }],
      sources: [], accounts: [{ account_id: "acct-cash", class: "asset", before_minor: "100000", change_minor: "225000", after_minor: "325000" }] },
  },
});

test("an agent's review link decodes to the same Book and proposal the engine encodes", async () => {
  const { decodeReviewReference } = await loadComponent("src/lib/proposals.ts");
  // The engine's own test vector (internal/operationref).
  assert.deepEqual(decodeReviewReference("AAAAAAAAAAAAAAAAAAAAAf____________________4"), {
    bookId: "00000000-0000-0000-0000-000000000001", proposalId: "ffffffff-ffff-ffff-ffff-fffffffffffe",
  });
  assert.equal(decodeReviewReference("not-a-reference"), null);
  assert.equal(decodeReviewReference(null), null);
});

test("the queue shows the exact entry, waits before sending, can be undone, and approving posts it", async () => {
  await withDOM(async () => {
    const { Approvals } = await loadComponent("src/components/approvals/approvals.tsx");
    const original = globalThis.fetch, posts = [];
    let refuse = true;
    globalThis.fetch = async (url, init) => {
      const u = new URL(String(url), "http://localhost");
      if (u.pathname === "/auth/session") return json({ mode: "local", user_id: "owner-1", csrf: "token" });
      if (init?.method === "POST") {
        posts.push({ path: u.pathname, body: JSON.parse(init.body) });
        if (refuse) { refuse = false; return json({ error: "fresh_authentication_required", sign_in_href: "/auth/login?operation=x" }, 403); }
        return json({ version: 1, decision: { proposal_id: "p-2", revision: 1, decision: "approve", reason: "ok", recorded_at: later(0) }, status: "committed", outcome: { status: "recorded", operation: "post_journal", result: { id: "entry-1" } } });
      }
      if (u.pathname === "/api/v1/agent-owner/plans")
        return json({ version: 1, book_id: book.id, plans: [summary("p-1", "committed", 3, "Older posted entry"), summary("p-2", posts.length > 1 ? "committed" : "approval_required", 2, "Client retainer")], next_before: 0, has_more: false });
      if (u.pathname === "/api/v1/agent-owner/plans/p-2") return json(detail("p-2", "approval_required"));
      return json({ error: "not found" }, 404);
    };
    const realNow = Date.now;
    let shift = 0;
    Date.now = () => realNow() + shift;
    const passUndo = async () => { shift += 6000; await React.act(async () => new Promise((r) => setTimeout(r, 1100))); await settle(); await settle(); };
    try {
      const root = document.getElementById("root");
      const { createRoot } = await import("react-dom/client");
      await React.act(async () => createRoot(root).render(React.createElement(Approvals, { book, state, initialProposal: "p-2" })));
      await settle(); await settle();
      assert.match(root.querySelector(".approval-card").textContent, /Client retainer/, "waiting work is listed first");
      assert.match(root.textContent, /Checking.*\$2,250\.00/, "lines show account names and exact amounts");
      assert.match(root.textContent, /\$1,000\.00 → \$3,250\.00/, "balance after");
      assert.match(root.textContent, /Is this a project payment\?/, "the agent's question is shown");
      // Reject needs a reason.
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Reject").click());
      assert.match(root.textContent, /Say what should change/);
      assert.equal(posts.length, 0);
      // Approve waits; Undo cancels before anything is sent.
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Approve").click());
      assert.match(root.textContent, /Approving in \d+s/);
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Undo").click());
      await passUndo();
      assert.equal(posts.length, 0, "an undone decision is never sent");
      // Approve again and let it send: a fresh sign-in is asked for, never assumed.
      await React.act(async () => [...root.querySelectorAll(".approval-row")].find((r) => /Client retainer/.test(r.textContent)).click());
      await settle();
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Approve").click());
      await passUndo();
      assert.equal(posts[0].path, "/api/v1/agent-owner/approve");
      assert.equal("executor_connection_id" in posts[0].body, false, "approving posts directly; no agent is named to post it");
      assert.equal(posts[0].body.plan_id, "p-2");
      assert.equal(posts[0].body.reason, "Approved");
      assert.match(root.textContent, /Sign in again/);
      await React.act(async () => [...root.querySelectorAll("button")].find((b) => b.textContent === "Approve").click());
      await passUndo();
      assert.equal(posts.length, 2);
      assert.match(root.querySelector(".approvals-log").textContent, /Client retainer.*Posted/);
    } finally {
      Date.now = realNow;
      globalThis.fetch = original;
    }
  });
});
