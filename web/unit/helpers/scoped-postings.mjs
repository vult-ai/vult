import { loadComponent } from "./dom.mjs";

// Preserve existing independent CSV fixture values while adapting mocked HTTP
// transport to the scoped JSON read. The original CSV parser tests remain.
const { parseTransactionJournal } = await loadComponent(
  "src/lib/transaction-journal.ts",
);
export function scopedPostingResponse(source, ids) {
  const [book_id, currency] = source.split("\r\n")[1].split(",");
  const journal = parseTransactionJournal(source, { id: book_id, currency });
  const wanted = ids && new Set(ids);
  return {
    ok: true,
    headers: new Headers({ "content-type": "application/json" }),
    json: async () => ({
      version: 1,
      book_id,
      currency,
      scale: 2,
      content_sha256: "a".repeat(64),
      entries: [...journal]
        .filter(([id]) => !wanted || wanted.has(id))
        .map(([id, lines]) => ({
          id,
          lines: lines.map((line) => ({
            id: line.id,
            account_code: line.account,
            side: line.amount < 0n ? "credit" : "debit",
            amount_minor: (line.amount < 0n
              ? -line.amount
              : line.amount
            ).toString(),
          })),
        })),
    }),
  };
}
