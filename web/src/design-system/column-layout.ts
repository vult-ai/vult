/** Shared presentation-only column sizing. Viewport changes never resize data. */
export type ColumnSize = {
  id: string;
  min: number;
  preferred: number;
  max?: number;
  fixed?: boolean;
  grow?: boolean;
};
export type ColumnWidths = Readonly<Record<string, number>>;
export const COLUMN_WIDTH_LIMIT = 4096;
const SAVED_COLUMN_LIMIT = 32;

export function columnBounds(column: ColumnSize) {
  return column.fixed
    ? { min: column.preferred, max: column.preferred }
    : { min: column.min, max: Math.max(column.min, column.max ?? Math.max(column.preferred, COLUMN_WIDTH_LIMIT)) };
}
export function resolveColumnWidths(columns: readonly ColumnSize[], saved: ColumnWidths = {}): ColumnWidths {
  return Object.fromEntries(columns.map(column => {
    const { min, max } = columnBounds(column);
    const candidate = saved[column.id];
    const value = Number.isFinite(candidate) ? candidate : column.preferred;
    return [column.id, Math.max(min, Math.min(max, column.fixed ? column.preferred : value))];
  }));
}
export function resizeColumn(columns: readonly ColumnSize[], before: ColumnWidths, id: string, requested: number): ColumnWidths {
  const column = columns.find(column => column.id === id);
  if (!column || column.fixed || !Number.isFinite(requested)) return before;
  const { min, max } = columnBounds(column);
  const width = Math.max(min, Math.min(max, requested));
  return width === before[id] ? before : { ...before, [id]: width };
}
export function retainColumnWidths(saved: ColumnWidths, visible: ColumnWidths): Record<string, number> {
  return Object.fromEntries([...Object.entries(saved).filter(([id]) => visible[id] === undefined), ...Object.entries(visible)].slice(-SAVED_COLUMN_LIMIT));
}
export function columnWidthTotal(columns: readonly ColumnSize[], widths: ColumnWidths) {
  return columns.reduce((sum, column) => sum + widths[column.id], 0);
}
/** Explicit Fit respects utility tracks, readable minima and non-growing metadata. */
export function fitColumnWidths(columns: readonly ColumnSize[], baseline: ColumnWidths, available: number): ColumnWidths {
  const next = { ...resolveColumnWidths(columns, baseline) };
  if (!Number.isFinite(available) || available <= 0) return next;
  for (let pass = 0; pass <= columns.length; pass++) {
    const delta = available - columnWidthTotal(columns, next);
    if (Math.abs(delta) < 0.01) break;
    const candidates = columns.filter(column => {
      const bounds = columnBounds(column);
      return !column.fixed && (delta > 0 ? column.grow !== false && next[column.id] < bounds.max : next[column.id] > bounds.min);
    });
    const weight = candidates.reduce((sum, column) => sum + next[column.id], 0);
    if (!weight) break;
    for (const column of candidates) {
      const bounds = columnBounds(column);
      next[column.id] = Math.max(bounds.min, Math.min(bounds.max, next[column.id] + delta * next[column.id] / weight));
    }
  }
  return next;
}
export function validColumnLayout(value: unknown): value is Record<string, number> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.entries(value).length <= SAVED_COLUMN_LIMIT &&
    Object.entries(value).every(([key, width]) => key.length <= 160 && key !== "__proto__" && key !== "constructor" && typeof width === "number" && Number.isFinite(width) && width > 0 && width <= COLUMN_WIDTH_LIMIT);
}
