import { useEffect, useMemo, useRef, useState } from "react";
import { get, type Book } from "../../lib/api";
import { readBookState } from "../../lib/book-state";
import { currencyScale } from "../../lib/book-currency";
import { readDocuments, uploadDocument } from "../../lib/documents";
import { fetchJSON } from "../../lib/errors";
import { readProposals } from "../../lib/proposals";
import { readSession, writeHeaders } from "../../lib/session";
import { settingsPost } from "../../lib/settings-requests";
import { withRecentSignIn } from "../../lib/recent-sign-in";
import { needsName } from "./welcome";
import { Entrance, type AgentEvent, type Journey } from "./entrance";

type Connection = { id: string; name: string; connection?: boolean; revoked_at?: string };

const POLL_MS = 2000;
// With entries recorded, this long without news means the agent has finished.
const QUIET_MS = 20_000;
const sleep = (ms: number) => new Promise<void>((resolve) => window.setTimeout(resolve, ms));
const progressKey = (bookId: string) => `vult.entrance.${bookId}`;

function remember(bookId: string, value: "started" | "skipped") {
  try {
    window.localStorage.setItem(progressKey(bookId), value);
  } catch {
    /* Without storage a reload starts inside the Book. */
  }
}
function forget(bookId: string) {
  try {
    window.localStorage.removeItem(progressKey(bookId));
  } catch {
    /* Nothing kept. */
  }
}
function skipped(bookId: string) {
  try {
    return window.localStorage.getItem(progressKey(bookId)) === "skipped";
  } catch {
    return false;
  }
}

async function connectedAgent() {
  const page = await get<{ tokens: Connection[] }>("access/tokens");
  return page.tokens.find((t) => t.connection !== false && !t.revoked_at)?.name;
}

/** The first run on a Vult server. Sign-in, the passkey and recovery codes
 * happen before the owner arrives, so the scene begins at the Book. The owner
 * lets the agent in on the agent's own sign-in page, and the scene follows
 * what the agent records. */
export function HostedEntrance({ book: existing, onOpen }: { book?: Book; onOpen: (book: Book, page: "dashboard" | "approvals") => void }) {
  const bookRef = useRef<Book | null>(existing ?? null);
  const [address, setAddress] = useState<string>();
  const settingsKey = useRef(crypto.randomUUID());
  const createKey = useRef(crypto.randomUUID());

  useEffect(() => {
    const controller = new AbortController();
    readSession(controller.signal).then(
      (s) => setAddress(s.agent_address ?? ""),
      () => { if (!controller.signal.aborted) setAddress(""); },
    );
    return () => controller.abort();
  }, []);

  const journey = useMemo<Journey>(() => {
    const book = () => {
      if (!bookRef.current) throw new Error("Create the Book first.");
      return bookRef.current;
    };
    return {
      connection: { kind: "server" as const, address: address ?? "" },
      async waitForAgent() {
        for (;;) {
          try {
            const name = await connectedAgent();
            if (name) return name;
          } catch {
            /* Keep waiting through a brief interruption. */
          }
          await sleep(POLL_MS * 2);
        }
      },
      // The owner already let the agent in on its sign-in page.
      async approveAgent() {},
      skipStatement() {
        remember(book().id, "skipped");
      },
      async addStatement(file) {
        const id = book().id;
        await uploadDocument(id, file, crypto.randomUUID());
        remember(id, "started");
      },
      async watchAgent(onEvent) {
        const current = book();
        const accounts = new Set<string>();
        const entries = new Set<string>();
        let waiting = 0;
        let quietSince = Date.now();
        let first = true;
        for (;;) {
          try {
            const state = await readBookState(current.id, {}, current.accounting_timezone, { view: "transactions", limit: 200 });
            const news: AgentEvent[] = [];
            // Accounts the Book already had are not news.
            for (const a of state.accounts ?? []) {
              if (accounts.has(a.code)) continue;
              accounts.add(a.code);
              if (!first) news.push({ kind: "account", name: a.name });
            }
            const recorded = [...(state.transactions ?? [])].sort((a, b) => a.date.localeCompare(b.date));
            for (const t of recorded) {
              if (entries.has(t.id)) continue;
              entries.add(t.id);
              news.push({ kind: "entry", entry: { date: t.date, description: t.description, amountMinor: t.amount_minor } });
            }
            const proposals = await readProposals(current.id);
            for (const p of proposals.plans.filter((p) => p.status === "approval_required").slice(waiting)) {
              news.push({ kind: "review", description: p.description || "An entry" });
            }
            waiting = Math.max(waiting, proposals.waiting);
            first = false;
            for (const event of news) onEvent(event);
            if (news.length) quietSince = Date.now();
          } catch {
            /* A missed poll catches up on the next one. */
          }
          if (entries.size > 0 && Date.now() - quietSince > QUIET_MS) return;
          await sleep(POLL_MS);
        }
      },
      async resume() {
        const current = bookRef.current;
        if (!current) return null;
        const [docs, agent] = await Promise.all([readDocuments(current.id), connectedAgent().catch(() => undefined)]);
        const latest = docs.documents.filter((d) => d.state === "ready").at(-1);
        if (!latest && !skipped(current.id) && !agent) return null;
        return { file: latest?.name ?? "", skipped: !latest && skipped(current.id), agent, allowed: !!agent };
      },
    };
  }, [address]);

  if (address === undefined) return null;
  // A Book that exists but was never named starts at naming it, not inside it.
  const unnamed = existing && needsName(existing) ? existing : undefined;
  return (
    <Entrance
      start={existing && !unnamed ? "inside" : "book"}
      book={existing && !unnamed ? { name: existing.name, profile: existing.profile, currency: existing.currency } : undefined}
      unnamed={unnamed ? { profile: existing?.profile, currency: unnamed.currency } : undefined}
      journey={journey}
      createBook={async ({ name, profile, currency, timezone }) => {
        if (unnamed) {
          // Naming a Book changes its settings, which need a recent sign-in.
          bookRef.current = await withRecentSignIn(() =>
            settingsPost<Book>("settings", {
              book_id: unnamed.id,
              idempotency_key: settingsKey.current,
              revision: unnamed.metadata_revision ?? 0,
              name,
              founder: "",
              profile,
              accounting_timezone: timezone,
            }),
          );
          remember(unnamed.id, "started");
          return;
        }
        const created = await withRecentSignIn(async () =>
          fetchJSON<Book>("/api/v1/books", {
            method: "POST",
            headers: await writeHeaders(),
            body: JSON.stringify({ request_key: createKey.current, name, currency, scale: currencyScale(currency) }),
          }),
        );
        bookRef.current = created;
        remember(created.id, "started");
        try {
          bookRef.current = await settingsPost<Book>("settings", {
            book_id: created.id,
            idempotency_key: settingsKey.current,
            revision: 0,
            name,
            founder: "",
            profile,
            accounting_timezone: timezone,
          });
        } catch {
          /* The Book exists; its details can be finished in its settings. */
        }
      }}
      onOpen={(page) => {
        const current = bookRef.current;
        if (!current) return;
        forget(current.id);
        onOpen(current, page);
      }}
    />
  );
}
