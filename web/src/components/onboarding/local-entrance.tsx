import { useEffect, useMemo, useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { currencyScale } from "../../lib/book-currency";
import { readDocuments, uploadDocument } from "../../lib/documents";
import { fetchJSON } from "../../lib/errors";
import { writeHeaders } from "../../lib/session";
import { settingsPost } from "../../lib/settings-requests";
import { withRecentSignIn } from "../../lib/recent-sign-in";
import { LOCAL_PROGRAM } from "../../lib/local-program";
import { needsName } from "./welcome";
import { Entrance, type AgentEvent, type EntranceDraft, type Journey } from "./entrance";

type Activity = {
  seq: number;
  agent: string;
  kind: string;
  document?: string;
  name?: string;
  date?: string;
  description?: string;
  reason?: string;
};
export type ActivityPage = {
  events: Activity[];
  next: number;
  waiting: { request: string; client: string }[];
  connected: string[];
};

const POLL_MS = 1500;
// With entries recorded, this long without news means the agent has finished.
const QUIET_MS = 30_000;
const SETTLE_MS = 6_000;

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));
const progressKey = (bookId: string) => `vult.entrance.${bookId}`;

/** The first run continues inside a new Book until the owner opens it. */
export function entranceStarted(bookId: string) {
  try {
    return window.localStorage.getItem(progressKey(bookId)) !== null;
  } catch {
    return false;
  }
}
function remember(bookId: string, value: "started" | "skipped") {
  try {
    window.localStorage.setItem(progressKey(bookId), value);
  } catch {
    /* Without storage a reload starts at the Book. */
  }
}
/** Ends the first run for a Book, so it opens as the Book from now on. */
export function finishEntrance(bookId: string) {
  forget(bookId);
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

export function readActivity(bookId: string, after = 0) {
  return fetchJSON<ActivityPage>(`/api/v1/local/agent-activity?book_id=${encodeURIComponent(bookId)}${after ? `&after=${after}` : ""}`, { cache: "no-store" });
}

export { withRecentSignIn };

function toEvent(a: Activity): AgentEvent | null {
  switch (a.kind) {
    case "opened":
      return a.document ? { kind: "opened", document: a.document } : null;
    case "checked":
      return { kind: "checked" };
    case "account":
      return a.name ? { kind: "account", name: a.name } : null;
    case "entry":
      return { kind: "entry", entry: { date: a.date ?? "", description: a.description ?? "" } };
    case "review":
      return { kind: "review", description: a.description || "An entry" };
    case "compared":
      return { kind: "compared" };
    case "balanced":
      return { kind: "balanced" };
    case "unusable":
      return { kind: "unusable", reason: a.reason || "It isn’t a statement it can record from." };
  }
  return null;
}

/** The first-run scene on this Mac, running on the real Vult: the Book is
 * created here, the statement is uploaded into it, the agent is let in after a
 * recent passkey sign-in, and every step shown is one the service saw. */
export function LocalEntrance({ book: existing, onOpen }: { book?: Book; onOpen: (book: Book, page: "dashboard" | "approvals") => void }) {
  const bookRef = useRef<Book | null>(existing ?? null);
  const [draft, setDraft] = useState<EntranceDraft | null | undefined>(existing ? null : undefined);
  const settingsKey = useRef(crypto.randomUUID());

  // An agent may have drafted the Book; nothing exists until the owner confirms.
  useEffect(() => {
    if (existing) return;
    const controller = new AbortController();
    fetchJSON<EntranceDraft | null>("/api/v1/local/book-draft", { signal: controller.signal }).then(
      (d) => setDraft(d),
      () => { if (!controller.signal.aborted) setDraft(null); },
    );
    return () => controller.abort();
  }, [existing]);

  const journey = useMemo<Journey>(() => {
    const bookId = () => {
      if (!bookRef.current) throw new Error("Create the Book first.");
      return bookRef.current.id;
    };
    let request = "";
    return {
      connection: { kind: "local", program: LOCAL_PROGRAM },
      async waitForAgent() {
        for (;;) {
          try {
            const page = await readActivity(bookId());
            if (page.waiting.length) {
              request = page.waiting[page.waiting.length - 1].request;
              return page.waiting[page.waiting.length - 1].client;
            }
            if (page.connected.length) return page.connected[0];
          } catch {
            /* Keep waiting through a brief interruption. */
          }
          await sleep(POLL_MS * 2);
        }
      },
      async approveAgent() {
        const id = bookId();
        const page = await readActivity(id);
        if (!page.waiting.length && page.connected.length) return;
        const waiting = page.waiting.find((w) => w.request === request) ?? page.waiting[page.waiting.length - 1];
        if (!waiting) throw new Error("The agent’s request ended. Ask it to use Vult again.");
        await withRecentSignIn(async () =>
          fetchJSON("/api/v1/local/agent-decision", {
            method: "POST",
            headers: await writeHeaders(),
            body: JSON.stringify({ request: waiting.request, book_id: id, access: "write" }),
          }),
        );
      },
      skipStatement() {
        remember(bookId(), "skipped");
      },
      async addStatement(file) {
        const id = bookId();
        await uploadDocument(id, file, crypto.randomUUID());
        remember(id, "started");
      },
      async watchAgent(onEvent) {
        const id = bookId();
        let after = 0;
        let entries = 0;
        let quietSince = Date.now();
        let last = "";
        for (;;) {
          try {
            const page = await readActivity(id, after);
            for (const a of page.events) {
              const event = toEvent(a);
              if (!event) continue;
              onEvent(event);
              quietSince = Date.now();
              if (event.kind === "entry") entries += 1;
              if (event.kind === "unusable") return;
              last = event.kind;
            }
            after = Math.max(after, page.next);
          } catch {
            /* A missed poll catches up on the next one. */
          }
          // A balance check after recording is usually the agent's last step;
          // a short pause after it ends the run, a longer one ends it anyway.
          const quiet = Date.now() - quietSince;
          if (entries > 0 && (quiet > QUIET_MS || ((last === "balanced" || last === "compared") && quiet > SETTLE_MS))) return;
          await sleep(POLL_MS);
        }
      },
      async resume() {
        const book = bookRef.current;
        if (!book) return null;
        const [docs, page] = await Promise.all([readDocuments(book.id), readActivity(book.id)]);
        const latest = docs.documents.filter((d) => d.state === "ready").at(-1);
        const agent = page.connected[0] ?? page.waiting.at(-1)?.client;
        if (page.waiting.length) request = page.waiting[page.waiting.length - 1].request;
        if (!latest && !skipped(book.id) && !page.connected.length) return null;
        return { file: latest?.name ?? "", skipped: !latest && skipped(book.id), agent, allowed: page.connected.length > 0 };
      },
    };
  }, []);

  if (draft === undefined) return null;
  const book = bookRef.current;
  // A Book that exists but was never named starts at naming it, not inside it.
  const unnamed = existing && needsName(existing) ? existing : undefined;
  return (
    <Entrance
      start={existing && !unnamed ? "inside" : "book"}
      draft={draft}
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
          fetchJSON<Book>("/api/v1/local/books", {
            method: "POST",
            headers: await writeHeaders(),
            body: JSON.stringify({ name, currency, scale: currencyScale(currency) }),
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
        const current = bookRef.current ?? book;
        if (!current) return;
        forget(current.id);
        onOpen(current, page);
      }}
    />
  );
}
