type VerticalBounds = { top: number; bottom: number };

/** Below this, a list is too short to use; above it, it opens downward
 * rather than covering the field's own label. */
const usableHeight = 160;

export function choiceMenuPosition(
  trigger: VerticalBounds,
  boundary: VerticalBounds,
  viewport: VerticalBounds,
  maxHeight = 256,
) {
  // Include the menu border and fractional positioning rounding in the inset.
  const top = Math.max(boundary.top, viewport.top) + 10;
  const bottom = Math.min(boundary.bottom, viewport.bottom) - 10;
  const above = Math.max(0, trigger.top - top - 4);
  const below = Math.max(0, bottom - trigger.bottom - 4);
  const openAbove = below < Math.min(maxHeight, usableHeight) && above > below;
  return {
    placement: openAbove ? ("top start" as const) : ("bottom start" as const),
    maxHeight: Math.min(maxHeight, openAbove ? above : below),
  };
}
