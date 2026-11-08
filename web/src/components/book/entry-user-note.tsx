import type { Transaction } from "../../lib/api";

// Explicit channel only: legacy audit text is never a display fallback.
export function EntryUserNote({
  entry,
}: {
  entry: Pick<Transaction, "user_note">;
}) {
  if (!entry.user_note) return null;
  return (
    <dl className="meta" aria-label="Note">
      <dt>Note</dt>
      <dd className="preserve-space">{entry.user_note}</dd>
    </dl>
  );
}
