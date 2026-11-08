// Radix owns modal semantics; the retained surface owns its real CSS transition.
import * as DialogPrimitive from "@radix-ui/react-dialog";
import {
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { XClose as X } from "../../design-system/icons";
import { useRetainedPresence } from "../../design-system/use-retained-presence";
import { focusWithoutScroll } from "../../design-system/focus-without-scroll";

/** Dialog standard v1 (docs/design/dialogs.md): a dialog picks one of the
 * shared widths and its height follows its content. Screens never size one. */
export type DialogSize = "small" | "medium" | "large" | "viewer";

type DialogProps = {
  title: string;
  /** One line under the title. Leave it out when the title says enough. */
  description?: string | null;
  size?: DialogSize;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  returnTo?: HTMLElement | null;
  children: ReactNode;
  variant?: "dialog" | "navigation";
  contentKey?: string;
  className?: string;
};

const PendingContext = createContext<
  ((owner: symbol, pending: boolean) => void) | null
>(null);

/** Forms report operation lifetime without exposing requests or records to the frame. */
export function useDialogPending(pending: boolean) {
  const report = useContext(PendingContext);
  const owner = useRef(Symbol());
  useLayoutEffect(() => {
    report?.(owner.current, pending);
    return () => report?.(owner.current, false);
  }, [pending, report]);
}

export function Dialog(props: DialogProps) {
  const owners = useRef(new Set<symbol>());
  const [busy, setBusy] = useState(false);
  const report = useCallback((owner: symbol, pending: boolean) => {
    if (pending) owners.current.add(owner);
    else owners.current.delete(owner);
    setBusy(owners.current.size > 0);
  }, []);
  return (
    <PendingContext.Provider value={report}>
      <DialogPrimitive.Root
        open={props.open}
        onOpenChange={(open) => {
          if (open || !busy) props.onOpenChange(open);
        }}
      >
        <DialogPrimitive.Portal forceMount>
          <DialogSurface {...props} busy={busy} />
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </PendingContext.Provider>
  );
}

function DialogSurface({
  title,
  description,
  open,
  returnTo,
  children,
  variant = "dialog",
  size = "medium",
  contentKey = title,
  className = "",
  busy,
}: DialogProps & { busy: boolean }) {
  const returnFocus = useRef<HTMLElement | null>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const previousKey = useRef(contentKey);
  const present = useRetainedPresence(open, contentRef);
  const retained = useRef({ children, title, description });
  if (open) retained.current = { children, title, description };
  const content = open ? { children, title, description } : retained.current;
  useLayoutEffect(() => {
    if (!open) return;
    if (previousKey.current !== contentKey && bodyRef.current)
      bodyRef.current.scrollTop = 0;
    previousKey.current = contentKey;
    const frame = requestAnimationFrame(() => {
      if (bodyRef.current?.contains(document.activeElement)) return;
      titleRef.current?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [contentKey, open]);
  if (!present) return null;
  return (
    <>
      <DialogPrimitive.Overlay forceMount className="dialog-overlay" />
      <DialogPrimitive.Content
        forceMount
        ref={contentRef}
        className={`dialog-content ${variant === "navigation" ? "dialog-navigation" : ""} ${className}`}
        data-size={variant === "navigation" ? undefined : size}
        inert={!open}
        aria-hidden={!open || undefined}
        {...(!content.description ? { "aria-describedby": undefined } : {})}
        onEscapeKeyDown={(event) => {
          // Radix listens in document capture, before React Aria's selector.
          // Let the topmost selector consume Escape without retiring its form.
          if (contentRef.current?.querySelector(".vult-select-menu"))
            event.preventDefault();
        }}
        onOpenAutoFocus={() => {
          returnFocus.current = document.activeElement as HTMLElement;
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          const target = returnTo || returnFocus.current;
          const openDialog = [...document.querySelectorAll('[role="dialog"][data-state="open"]')].at(-1);
          if (openDialog && (!target || !openDialog.contains(target))) return;
          if (target?.isConnected) {
            const row = target.closest<HTMLElement>('tr[data-record-id]');
            if (row) focusWithoutScroll(row);
            focusWithoutScroll(target);
          }
        }}
      >
        <div className="dialog-heading">
          <DialogPrimitive.Title ref={titleRef} tabIndex={-1}>
            {content.title}
          </DialogPrimitive.Title>
          {content.description && (
            <DialogPrimitive.Description>
              {content.description}
            </DialogPrimitive.Description>
          )}
          <DialogPrimitive.Close
            disabled={busy}
            className="dialog-close"
            aria-label="Close dialog"
          >
            <X className="size-4" />
          </DialogPrimitive.Close>
        </div>
        <div ref={bodyRef} className="dialog-body">
          {content.children}
        </div>
      </DialogPrimitive.Content>
    </>
  );
}

/** The dialog's actions: Cancel first, then the one primary action, at the end. */
export function DialogFooter({ children }: { children: ReactNode }) {
  return <div className="dialog-footer">{children}</div>;
}
