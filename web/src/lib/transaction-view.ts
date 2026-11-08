/** Legacy snapshots derive counts locally. Paged responses carry counts for the
 * complete matching collection; never mistake visible rows for the full total. */
export function transactionView<T extends { kind: string; view_kind?: string }>(
  rows: T[],
  kind: string,
  serverCounts?: Record<string, number>,
) {
  const counts: Record<string, number> = { "": rows.length };
  for (const row of rows) {
    const view = row.view_kind || row.kind;
    counts[view] = (counts[view] || 0) + 1;
    if (row.kind !== view) counts[row.kind] = (counts[row.kind] || 0) + 1;
  }
  return {
    rows: kind
      ? rows.filter(
          (row) => row.kind === kind || (row.view_kind || row.kind) === kind,
        )
      : rows,
    counts: serverCounts ?? counts,
  };
}
