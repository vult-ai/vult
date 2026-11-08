import { useEffect, useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { APIError, fetchJSON } from "../../lib/errors";
import { writeHeaders } from "../../lib/session";
import { Button } from "../ui/button";
import { readActivity, withRecentSignIn } from "./local-entrance";
import "./agent-request-bar.css";

const POLL_MS = 3000;

/** On this Mac, an agent asking to come in shows at the top of the Book, so
 * the owner lets it in where they already are. Nothing shows on a server. */
export function AgentRequestBar({ book }: { book: Book }) {
  const [waiting, setWaiting] = useState<{ request: string; client: string } | null>(null);
  const [busy, setBusy] = useState<"" | "allow" | "deny">("");
  const [error, setError] = useState("");
  const answered = useRef(new Set<string>());

  useEffect(() => {
    let live = true;
    let timer = 0;
    // Only waiting agents matter here, so each read skips the steps already seen.
    let after = 0;
    const check = async () => {
      if (document.visibilityState === "visible") {
        try {
          const page = await readActivity(book.id, after);
          if (!live) return;
          after = Math.max(after, page.next);
          const next = page.waiting.filter((w) => !answered.current.has(w.request)).at(-1) ?? null;
          setWaiting((current) => (current?.request === next?.request ? current : next));
        } catch (e) {
          // Only this Mac's Vult answers; elsewhere the bar stays away.
          if (e instanceof APIError && e.status === 404) return;
        }
      }
      if (live) timer = window.setTimeout(() => void check(), POLL_MS);
    };
    void check();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [book.id]);

  if (!waiting) return null;
  async function decide(deny: boolean) {
    if (!waiting) return;
    setBusy(deny ? "deny" : "allow");
    setError("");
    try {
      const send = async () =>
        fetchJSON("/api/v1/local/agent-decision", {
          method: "POST",
          headers: await writeHeaders(),
          body: JSON.stringify(deny ? { request: waiting.request, deny: true } : { request: waiting.request, book_id: book.id, access: "write" }),
        });
      await (deny ? send() : withRecentSignIn(send));
      answered.current.add(waiting.request);
      setWaiting(null);
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : "That didn’t go through. Try again.");
    } finally {
      setBusy("");
    }
  }
  return (
    <div className="agent-request-bar" role="region" aria-label="Agent asking to come in">
      <span className="agent-request-avatar" aria-hidden="true">{waiting.client.slice(0, 1)}</span>
      <span className="agent-request-text">
        <strong>{waiting.client} wants to come in</strong>
        <small>{error || "It records entries. Anything it isn’t sure about waits for you."}</small>
      </span>
      <Button color="tertiary" size="sm" busy={busy === "deny"} disabled={!!busy} onClick={() => void decide(true)}>
        Don’t allow
      </Button>
      <Button size="sm" busy={busy === "allow"} disabled={!!busy} onClick={() => void decide(false)}>
        Allow
      </Button>
    </div>
  );
}
