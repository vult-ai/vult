import { useLayoutEffect, useRef, type ReactNode } from "react";
import { File06 } from "../../design-system/icons";
import {
  InspectorPanel,
  captureInspectorScroll,
  restoreInspectorScroll,
  type InspectorScroll,
} from "../../design-system/collection-inspector";
import { focusWithoutScroll } from "../../design-system/focus-without-scroll";
import "./entry-preview.css";

import { Dialog, useDialogPending } from "../ui/dialog";
import { EmptyState } from "../ui/empty-state";

function ContextualEntryContent({pending,children}:{pending:boolean;children:ReactNode}){useDialogPending(pending);return children;}

/** Reuse the entry/evidence reader without navigating out of a review context. */
export function ContextualEntryPreview({id,pending,returnTo,onClose,children}:{id:string;pending:boolean;returnTo:HTMLElement|null;onClose:()=>void;children:(id:string)=>ReactNode}){
  const previous=useRef(false);const scroll=useRef<InspectorScroll|null>(null);
  if(id&&!previous.current)scroll.current=captureInspectorScroll(returnTo);
  useLayoutEffect(()=>{const closed=previous.current&&!id;previous.current=!!id;if(closed)return restoreInspectorScroll(scroll.current);},[id]);
  return <Dialog open={!!id} title="Entry details" description="Inspect the recorded entry and its available evidence. Closing returns to the same reporting context." contentKey={id} size="large" className="entry-context-dialog" returnTo={returnTo} onOpenChange={open=>{if(!open&&!pending)onClose();}}><ContextualEntryContent pending={pending}>{id ? children(id):null}</ContextualEntryContent></Dialog>;
}

export function EntryPreview({
  id,
  regionId,
  pending,
  returnTo,
  onClose,
  idle,
  idleTitle,
  children,
}: {
  id: string;
  regionId: string;
  /** Replaces the "Select a transaction" prompt while nothing is selected. */
  idle?: ReactNode;
  /** The panel heading while `idle` is shown. */
  idleTitle?: string;
  pending: boolean;
  returnTo: HTMLElement | null;
  onClose: () => void;
  children: (id: string) => ReactNode;
}) {
  const target = useRef<{
    id: string;
    element: HTMLElement;
    row: string;
    label: string | null;
    scroll: InspectorScroll | null;
  } | null>(null);
  const previous = useRef(Boolean(id));
  if (
    id &&
    returnTo?.isConnected &&
    (!previous.current ||
      target.current?.id !== id ||
      target.current.element !== returnTo)
  ) {
    target.current = {
      id,
      element: returnTo,
      row:
        returnTo
          .closest("tr[data-record-id]")
          ?.getAttribute("data-record-id") || "",
      label: returnTo.getAttribute("aria-label"),
      scroll:
        captureInspectorScroll(returnTo) || target.current?.scroll || null,
    };
  }
  if (!id && previous.current && target.current?.scroll) {
    // A docked list remains usable while inspecting. Keep its latest position;
    // a hidden compact list must retain the bookmark from before it was hidden.
    target.current.scroll =
      captureInspectorScroll(target.current.scroll.element) ||
      target.current.scroll;
  }
  useLayoutEffect(() => {
    const closed = previous.current && !id;
    previous.current = Boolean(id);
    if (!closed) return;
    const region = document
      .getElementById(regionId)
      ?.closest<HTMLElement>(".collection-inspector");
    if (!region || region.closest("[hidden], [inert]")) return;
    if (document.querySelector('[role="dialog"][data-state="open"]')) return;
    const saved = target.current;
    let destination = saved?.element;
    if (
      (!destination?.isConnected || !region.contains(destination)) &&
      saved?.row
    ) {
      const row = region.querySelector<HTMLElement>(
        `tr[data-record-id="${CSS.escape(saved.row)}"]`,
      );
      destination =
        [
          ...(row?.querySelectorAll<HTMLElement>("button[aria-label]") || []),
        ].find((button) => button.getAttribute("aria-label") === saved.label) ||
        row ||
        undefined;
    }
    if (
      !destination?.isConnected ||
      !region.contains(destination) ||
      destination.closest("[hidden], [inert]")
    ) {
      destination =
        document.querySelector<HTMLElement>("#main-content h1") || undefined;
    }
    // Layout effects run after the list is revealed, before the first paint.
    if (destination) {
      // Enter the grid's focus scope before restoring its nested control.
      // Otherwise React Aria can redirect focus to a different selected row.
      const row = destination.closest<HTMLElement>("tr[data-record-id]");
      if (row) focusWithoutScroll(row);
      focusWithoutScroll(destination);
      return restoreInspectorScroll(
        row?.getAttribute("data-record-id") === saved?.row
          ? saved?.scroll || null
          : null,
      );
    }
  }, [id, returnTo, regionId]);
  return (
    <InspectorPanel
      id={regionId}
      selectionKey={id}
      label="Entry details"
      closeLabel="Close entry details"
      backLabel="Back to transactions"
      testId="entry-preview"
      pending={pending}
      onClose={() => {
        if (!pending) onClose();
      }}
      emptyTitle={idle ? idleTitle : undefined}
      empty={
        idle ?? <div className="collection-inspector-empty">
          <EmptyState
            scope="region"
            icon={File06}
            title="Select a transaction"
            detail="Choose a row to see its details and attached document."
          />
        </div>
      }
    >
      <div className="entry-preview-content" aria-busy={pending}>
        {id && children(id)}
      </div>
    </InspectorPanel>
  );
}
