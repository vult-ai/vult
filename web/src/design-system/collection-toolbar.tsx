import type { ReactNode } from "react";
import "./collection-toolbar.css";

/** Toolbar standard v1 (docs/design/toolbars.md): the collection's identity
 * at the start, its actions at the end. Nothing is cut off; when the band is
 * too narrow, the actions wrap to a second row. */
export function CollectionToolbar({
  lead,
  actions,
  label,
}: {
  lead: ReactNode;
  actions?: ReactNode;
  label?: string;
}) {
  return (
    <div className="vult-collection-toolbar" role="group" aria-label={label}>
      <div className="vult-collection-toolbar-lead">{lead}</div>
      {actions && <div className="vult-collection-toolbar-actions">{actions}</div>}
    </div>
  );
}

/** A collection's name and count, as "Files 119" or "Accounts 212 · USD". */
export function CollectionTitle({
  label,
  count,
  meta,
  countLabel,
}: {
  label: ReactNode;
  /** Already formatted, so a pending or partial count can say so ("…", "1,200+"). */
  count?: ReactNode;
  meta?: ReactNode;
  /** Accessible wording for the count, such as "212 accounts". */
  countLabel?: string;
}) {
  return (
    <span className="vult-collection-title vult-type-label text-primary">
      {label}
      {count !== undefined && (
        <span className="vult-type-metadata text-tertiary" aria-label={countLabel}>
          {count}
        </span>
      )}
      {meta !== undefined && <span className="vult-type-metadata text-tertiary">· {meta}</span>}
    </span>
  );
}
