/** Activity rows and the count for each tab. The server applies the selected
 * tab itself (an entry is income or an expense when it touches an account of
 * that class), so its rows are shown as they come. Tab counts come from the
 * server for the same dates, search and account; without them, only the
 * loaded rows can be counted. */
export function transactionView<T extends { kind: string; view_kind?: string }>(
  rows: T[],
  _kind: string,
  serverCounts?: Record<string, number>,
) {
  const counts: Record<string, number> = { "": rows.length };
  for (const row of rows) {
    const view = row.view_kind || row.kind;
    counts[view] = (counts[view] || 0) + 1;
    if (row.kind !== view) counts[row.kind] = (counts[row.kind] || 0) + 1;
  }
  return { rows, counts: serverCounts ?? counts };
}
