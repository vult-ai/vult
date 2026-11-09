import { Lock01 } from "../../design-system/icons";
import "./book-period-settings.css";
import { useCallback, useEffect, useRef, useState } from "react";
import { bookURL, type Book } from "../../lib/api";
import { formatDate } from "../../lib/date-format";
import { fetchJSON } from "../../lib/errors";
import { writeHeaders } from "../../lib/session";
import { ErrorMessage, Field } from "../form";
import { Button } from "../ui/button";
import { ConfirmAction } from "../ui/confirm-action";
import { SettingsRow, SettingsSection } from "../ui/settings-section";

type ClosedPeriod = { id: string; from: string; through: string; closed_at: string };

/* Closing a period locks its dates: no entry dated inside it can be posted,
   reversed or changed afterwards, by the owner or any agent. It is permanent.
   Corrections after a close are recorded as new entries in an open period. */
export function BookPeriodSettings({ book }: { book: Book }) {
  const [periods, setPeriods] = useState<ClosedPeriod[] | null>(null);
  const [readError, setReadError] = useState<unknown>(null);
  const [from, setFrom] = useState("");
  const [through, setThrough] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const requestKey = useRef(crypto.randomUUID());
  const trigger = useRef<HTMLElement | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const out = await fetchJSON<{ periods: ClosedPeriod[] }>(`/api/v1/${bookURL("periods", book.id)}`, { cache: "no-store", ...(signal ? { signal } : {}) });
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

  async function close() {
    setBusy(true);
    setError(null);
    try {
      await fetchJSON("/api/v1/periods/close", {
        method: "POST",
        headers: await writeHeaders(),
        body: JSON.stringify({ request_key: requestKey.current, book_id: book.id, from, through }),
      });
      requestKey.current = crypto.randomUUID();
      setConfirming(false);
      setFrom("");
      setThrough("");
      await load();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsSection
      title="Closed periods" icon={Lock01}
      description="Locks every entry in its dates."
    >
      <SettingsRow label="Closed so far">
        {readError ? (
          <ErrorMessage message={readError} />
        ) : periods === null ? (
          <span>Loading…</span>
        ) : periods.length === 0 ? (
          <span>No periods closed yet</span>
        ) : (
          <ul className="book-period-list">
            {periods.map((p) => (
              <li key={p.id}>
                {formatDate(p.from, "full")} – {formatDate(p.through, "full")}
              </li>
            ))}
          </ul>
        )}
      </SettingsRow>
      <SettingsRow label="Close a period" description="Permanent.">
        <form
          className="book-period-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid) return;
            trigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
            setConfirming(true);
          }}
        >
          <Field label="From">
            <input name="from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </Field>
          <Field label="Through">
            <input name="through" type="date" value={through} min={from || undefined} onChange={(e) => setThrough(e.target.value)} />
          </Field>
          <Button type="submit" color="secondary" disabled={!valid}>
            Close period
          </Button>
        </form>
      </SettingsRow>
      <ConfirmAction
        open={confirming}
        title="Close this period?"
        detail={
          <>
            {valid ? `${formatDate(from, "full")} – ${formatDate(through, "full")}` : ""} in {book.name} will accept no more entries, from you or any agent. Corrections go in a later, open period.
          </>
        }
        irreversible
        confirmLabel="Close period"
        busy={busy}
        error={error}
        onConfirm={() => void close()}
        onCancel={() => {
          if (!busy) {
            setConfirming(false);
            setError(null);
          }
        }}
        returnTo={trigger.current}
      />
    </SettingsSection>
  );
}
