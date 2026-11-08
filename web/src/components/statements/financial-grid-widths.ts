export type GridColumnSize = { id: string; min: number; preferred: number; max: number };
export type GridWidths = Readonly<Record<string, number>>;
export const GRID_WIDTH_PREFERENCE_LIMIT = 4096;

export const gridWidthTotal = (sizes: readonly GridColumnSize[], widths: GridWidths) =>
  sizes.reduce((total, size) => total + widths[size.id], 0);

/** Like Activity, fit only the untouched layout. Manual pixel widths never
 * redistribute into a neighbor or expand again to fill the viewport. */
export function resolveGridWidths(sizes: readonly GridColumnSize[], saved: GridWidths, viewport: number, accountBase = 320): GridWidths {
  const widths = Object.fromEntries(sizes.map(size => [size.id, Math.max(size.min, saved[size.id] ?? size.preferred)]));
  if (sizes.length && !sizes.some(size => saved[size.id] !== undefined)) {
    const account = sizes[0], numericTotal = gridWidthTotal(sizes.slice(1), widths);
    widths[account.id] = Math.max(account.min, Math.min(account.max, Math.max(accountBase, viewport - numericTotal)));
  }
  return widths;
}

export function gridColumnBounds(sizes: readonly GridColumnSize[], widths: GridWidths, id: string) {
  const size = sizes.find(size => size.id === id);
  return size ? { min: size.min, max: Math.min(GRID_WIDTH_PREFERENCE_LIMIT, Math.max(size.max, widths[id])) } : undefined;
}

/** Preserve every other rendered width, exactly as Activity's controlled columns. */
export function resizeGridColumn(sizes: readonly GridColumnSize[], baseline: GridWidths, id: string, requested: number): GridWidths {
  const bounds = gridColumnBounds(sizes, baseline, id);
  if (!bounds || !Number.isFinite(requested)) return baseline;
  const value = Math.max(bounds.min, Math.min(bounds.max, requested));
  return value === baseline[id] ? baseline : { ...baseline, [id]: value };
}

/** Freeze the complete resolved layout on the first real resize; keep hidden
 * comparison/month widths without applying a second fitting pass. */
export function gridWidthPreferences(sizes: readonly GridColumnSize[], saved: GridWidths, changed: GridWidths): Record<string, number> {
  const retained = Object.fromEntries(sizes.map(size => [size.id, changed[size.id]]));
  return Object.fromEntries([
    ...Object.entries(saved).filter(([id]) => !Object.hasOwn(retained, id)),
    ...Object.entries(retained),
  ].slice(-32));
}
