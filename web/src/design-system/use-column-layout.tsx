import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode, type PointerEvent, type KeyboardEvent, type MouseEvent, type RefObject } from "react";
import { useOptionalPreferenceScope } from "../lib/user-preferences-context";
import { columnBounds, columnWidthTotal, fitColumnWidths, resolveColumnWidths, resizeColumn, retainColumnWidths, validColumnLayout, type ColumnSize, type ColumnWidths } from "./column-layout";

const BookContext = createContext<string | undefined>(undefined);
export function ColumnLayoutScope({ bookId, children }: { bookId: string; children: ReactNode }) {
  return <BookContext.Provider value={bookId}>{children}</BookContext.Provider>;
}
type Preferences = { identity: string; widths: ColumnWidths; update: (widths: Record<string, number>) => void };
function useColumnPreferences(view: string, external?: Preferences): Preferences {
  const owner = useOptionalPreferenceScope(), book = useContext(BookContext);
  const key = !external && owner?.kind === "user" && book
    ? `vult:column-layout:v1:${encodeURIComponent(owner.id)}:${encodeURIComponent(book)}:${encodeURIComponent(view)}` : null;
  const read = (): ColumnWidths => {
    if (!key) return {};
    try { const stored: unknown = JSON.parse(window.localStorage.getItem(key) || "null"); return validColumnLayout(stored) ? stored : {}; } catch { return {}; }
  };
  const identity = key ?? `${book || "memory"}:${view}`;
  const currentIdentity = useRef(identity), live = useRef(false);
  currentIdentity.current = identity;
  useLayoutEffect(() => { live.current = true; return () => { live.current = false; }; }, []);
  const [state, setState] = useState(() => ({ identity, widths: read() }));
  // Read the new owner synchronously; never render another Book's preferences.
  const widths = state.identity === identity ? state.widths : read();
  useLayoutEffect(() => { if (state.identity !== identity) setState({ identity, widths }); }, [identity]);
  useEffect(() => {
    if (!key) return;
    const changed = (event: StorageEvent) => {
      try { if ((event.key === key || event.key === null) && event.storageArea === window.localStorage) setState({ identity, widths: read() }); } catch { /* Optional storage may become unavailable. */ }
    };
    window.addEventListener("storage", changed);
    return () => window.removeEventListener("storage", changed);
  }, [key]);
  return external ?? { identity, widths, update: next => {
    if (!live.current || currentIdentity.current !== identity) return;
    setState({ identity, widths: next });
    if (key) try { window.localStorage.setItem(key, JSON.stringify(next)); } catch { /* Widths remain usable in this tab. */ }
  } };
}

export function useColumnLayout({ columns, view, available, scroll, external, disabled = false }: {
  columns: readonly ColumnSize[]; view: string; available: number;
  scroll: RefObject<HTMLElement | null>; external?: Preferences; disabled?: boolean;
}) {
  const preferences = useColumnPreferences(view, external);
  const token = JSON.stringify([preferences.identity, preferences.widths, columns, available, disabled]);
  const idle = resolveColumnWidths(columns, preferences.widths);
  const [draft, setDraft] = useState<{ token: string; widths: ColumnWidths }>();
  const widths = draft?.token === token ? draft.widths : idle;
  type Drag = { id: string; pointer: number; x: number; left: number; grip: number; baseline: ColumnWidths; token: string; viewport: number; target: HTMLDivElement };
  const drag = useRef<Drag | undefined>(undefined);
  function release() {
    const active = drag.current;
    drag.current = undefined;
    if (active?.target.hasPointerCapture?.(active.pointer)) active.target.releasePointerCapture(active.pointer);
    return active;
  }
  function cancel() { release(); setDraft(undefined); }
  useLayoutEffect(() => { if (drag.current && drag.current.token !== token) cancel(); }, [token]);
  useEffect(() => () => { release(); }, []);
  function remember(changed: ColumnWidths, baseline = widths) {
    if (columns.some(column => changed[column.id] !== baseline[column.id])) preferences.update(retainColumnWidths(preferences.widths, changed));
  }
  // The column's right edge follows the pointer. Measuring from the heading's
  // live left edge stays exact when the table scrolls or the browser shifts
  // the scroll position as the table narrows.
  const heading = (target: HTMLElement) => target.closest<HTMLElement>('th, [role="columnheader"]');
  function pointerWidth(event: PointerEvent<HTMLDivElement>, active: Drag) {
    const cell = heading(active.target);
    const requested = cell
      ? event.clientX - cell.getBoundingClientRect().left - active.grip
      : active.baseline[active.id] + event.clientX - active.x + (scroll.current?.scrollLeft || 0) - active.left;
    return resizeColumn(columns, active.baseline, active.id, requested);
  }
  function valid(active: Drag) { return active.token === token && active.viewport === (scroll.current?.clientWidth ?? available); }
  function reset(id: string) {
    cancel();
    const column = columns.find(column => column.id === id);
    if (column) remember(resizeColumn(columns, widths, id, column.preferred));
  }
  return {
    widths,
    // While a column is dragged narrower, the table keeps its starting width,
    // so the browser never slides the content under the pointer.
    extent: drag.current && draft?.token === token
      ? Math.max(columnWidthTotal(columns, widths), columnWidthTotal(columns, drag.current.baseline))
      : columnWidthTotal(columns, widths),
    canFit: available > 0 && !disabled,
    fit() { cancel(); remember(fitColumnWidths(columns, widths, available)); },
    reset() { cancel(); preferences.update({}); },
    cancel,
    resizer(id: string) {
      const column = columns.find(column => column.id === id)!;
      const bounds = columnBounds(column);
      return {
        value: widths[id], ...bounds, disabled: disabled || column.fixed,
        resizing: drag.current?.token === token && drag.current.id === id,
        onPointerDown(event: PointerEvent<HTMLDivElement>) {
          if (disabled || column.fixed || event.button !== 0 || drag.current) return;
          event.preventDefault(); event.stopPropagation();
          const target = event.currentTarget;
          const cell = heading(target);
          // Where on the handle the pointer took hold, relative to the column's edge.
          const grip = cell ? event.clientX - cell.getBoundingClientRect().left - widths[id] : 0;
          drag.current = { id, pointer: event.pointerId, x: event.clientX, left: scroll.current?.scrollLeft || 0, grip, baseline: widths, token, viewport: scroll.current?.clientWidth ?? available, target };
          setDraft({ token, widths });
          target.focus({ preventScroll: true }); target.setPointerCapture?.(event.pointerId);
        },
        onPointerMove(event: PointerEvent<HTMLDivElement>) {
          const active = drag.current;
          if (!active || active.pointer !== event.pointerId) return;
          if (!valid(active)) { cancel(); return; }
          setDraft({ token, widths: pointerWidth(event, active) });
        },
        onPointerUp(event: PointerEvent<HTMLDivElement>) {
          const active = drag.current;
          if (!active || active.pointer !== event.pointerId) return;
          if (!valid(active)) { cancel(); return; }
          const changed = pointerWidth(event, active);
          release(); setDraft(undefined); remember(changed, active.baseline);
        },
        onPointerCancel: cancel,
        onLostPointerCapture() { if (drag.current) cancel(); },
        onClick(event: MouseEvent<HTMLDivElement>) { event.preventDefault(); event.stopPropagation(); },
        onDoubleClick(event: MouseEvent<HTMLDivElement>) { event.preventDefault(); event.stopPropagation(); reset(id); },
        onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
          if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); cancel(); return; }
          if (disabled || column.fixed || drag.current) return;
          if (!["ArrowLeft", "ArrowRight", "Home", "End", "Enter"].includes(event.key)) return;
          event.preventDefault(); event.stopPropagation();
          if (event.key === "Enter") { reset(id); return; }
          const requested = event.key === "Home" ? bounds.min : event.key === "End" ? bounds.max : widths[id] + (event.key === "ArrowLeft" ? -1 : 1) * (event.shiftKey ? 32 : 8);
          remember(resizeColumn(columns, widths, id, requested));
        },
      };
    },
  };
}
export type ColumnLayout = ReturnType<typeof useColumnLayout>;
