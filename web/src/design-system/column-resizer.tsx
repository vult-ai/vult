import type { ColumnLayout } from "./use-column-layout";
import "./column-layout.css";

export function ColumnResizer({ layout, id, label, className = "" }: { layout: ColumnLayout; id: string; label: string; className?: string }) {
  const { value, min, max, disabled, resizing, ...events } = layout.resizer(id);
  return <div {...events} role="separator" tabIndex={disabled ? -1 : 0} className={`vult-column-resizer vult-focus-target ${className}`} data-resize-column={id} data-resizing={resizing || undefined} aria-disabled={disabled || undefined} aria-label={`Resize ${label} column`} aria-orientation="vertical" aria-valuenow={Math.round(value)} aria-valuemin={Math.ceil(min)} aria-valuemax={Math.floor(max)} title="Drag to resize. Double-click or press Enter to reset. Arrow keys resize; Shift uses larger steps." />;
}
