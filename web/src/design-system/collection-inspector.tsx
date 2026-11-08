import {
  createContext,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ArrowLeft } from "./icons";
import { Button } from "./catalog/components/vult-ui/button";
import { CloseButton } from "./catalog/components/vult-ui/close-button";
import { focusWithoutScroll } from "./focus-without-scroll";
import "./collection-inspector.css";

const InspectorContext = createContext({ compact: false, selected: false });

export type InspectorScroll = {
  element: HTMLElement;
  left: number;
  top: number;
  owner: HTMLElement;
  ancestors: { element: HTMLElement; left: number; top: number }[];
  page: { window: Window; left: number; top: number } | null;
};

const pendingRestorations = new WeakMap<HTMLElement, () => void>();

export function captureInspectorScroll(
  source: HTMLElement | null,
): InspectorScroll | null {
  if (!source?.isConnected || source.closest("[hidden], [inert]")) return null;
  const element = source.closest<HTMLElement>("[data-vult-view-scroll]") || source
    .closest("table")
    ?.querySelector<HTMLElement>("[data-vult-view-scroll]");
  const owner = element?.closest<HTMLElement>(".collection-inspector, .sources-workspace, [data-vult-context-inspector]");
  if (!element || !owner) return null;
  const ancestors: InspectorScroll["ancestors"] = [];
  for (
    let current: HTMLElement | null = element;
    current;
    current = current.parentElement
  )
    ancestors.push({
      element: current,
      left: current.scrollLeft,
      top: current.scrollTop,
    });
  const window = element.ownerDocument.defaultView;
  return {
    element,
    owner,
    ancestors,
    left: element.scrollLeft,
    top: element.scrollTop,
    page: window ? { window, left: window.scrollX, top: window.scrollY } : null,
  };
}

/** Grid focus can schedule its own scroll after preventScroll has returned. */
export function restoreInspectorScroll(position: InspectorScroll | null) {
  if (!position) return () => {};
  const { owner, element, page } = position;
  const document = owner.ownerDocument;
  const window = document.defaultView;
  pendingRestorations.get(owner)?.();
  let active = true;
  let frame = 0;
  let focusRevision = 0;
  let quietFrames = 0;
  const tracked = new Set(position.ancestors.map((saved) => saved.element));
  const inputs = ["pointerdown", "wheel", "touchstart", "keydown"] as const;
  const cancel = () => {
    if (!active) return;
    active = false;
    if (frame) window?.cancelAnimationFrame(frame);
    for (const input of inputs)
      document.removeEventListener(input, cancel, true);
    document.removeEventListener("focusin", onFocus, true);
    document.removeEventListener("scroll", onScroll, true);
    if (pendingRestorations.get(owner) === cancel)
      pendingRestorations.delete(owner);
  };
  const restore = () => {
    if (!active) return;
    if (
      !element.isConnected ||
      !owner.contains(element) ||
      owner.hasAttribute("data-selected") ||
      element.closest("[hidden], [inert]")
    ) {
      cancel();
      return;
    }
    for (const saved of position.ancestors) {
      if (!saved.element.isConnected) continue;
      if (saved.element.scrollLeft !== saved.left)
        saved.element.scrollLeft = saved.left;
      if (saved.element.scrollTop !== saved.top)
        saved.element.scrollTop = saved.top;
    }
    if (
      page &&
      (page.window.scrollX !== page.left || page.window.scrollY !== page.top)
    )
      page.window.scrollTo(page.left, page.top);
  };
  const onFocus = (event: FocusEvent) => {
    if (!(event.target instanceof Node) || !element.contains(event.target)) {
      cancel();
      return;
    }
    focusRevision++;
    quietFrames = 0;
  };
  const onScroll = (event: Event) => {
    if (event.target === document || tracked.has(event.target as HTMLElement))
      restore();
  };
  pendingRestorations.set(owner, cancel);
  for (const input of inputs)
    document.addEventListener(input, cancel, { capture: true, passive: true });
  document.addEventListener("focusin", onFocus, true);
  document.addEventListener("scroll", onScroll, {
    capture: true,
    passive: true,
  });
  restore();
  // Repair native scroll events before paint while the grid settles its focus.
  // Two quiet frames cover its follow-up focus pass; new user input always wins.
  queueMicrotask(() => queueMicrotask(restore));
  const settle = () => {
    if (!active || !window) return;
    frame = window.requestAnimationFrame(() => {
      frame = 0;
      const revision = focusRevision;
      restore();
      queueMicrotask(() =>
        queueMicrotask(() => {
          restore();
          if (!active) return;
          quietFrames = revision === focusRevision ? quietFrames + 1 : 0;
          if (quietFrames >= 2) cancel();
          else settle();
        }),
      );
    });
  };
  if (window) settle();
  else queueMicrotask(() => queueMicrotask(cancel));
  return cancel;
}

/** Documents and Transactions share one canvas and one adaptive pane policy. */
export function CollectionInspector({
  selected,
  pending = false,
  onClose,
  children,
}: {
  selected: boolean;
  pending?: boolean;
  onClose?: () => void;
  children: ReactNode;
}) {
  const root = useRef<HTMLDivElement>(null);
  const [compact, setCompact] = useState(false);
  useLayoutEffect(() => {
    const measure = () => {
      const width = root.current?.getBoundingClientRect().width || 0;
      // The panel docks only while the table beside it keeps its minimum
      // columns: about 800px of table plus the 304px panel.
      if (width) setCompact(width < 1120);
    };
    measure();
    const observer = new ResizeObserver(measure);
    if (root.current) observer.observe(root.current);
    return () => observer.disconnect();
  }, []);
  return (
    <InspectorContext.Provider value={{ compact, selected }}>
      <div
        ref={root}
        className="collection-inspector"
        data-compact={compact || undefined}
        data-selected={selected || undefined}
        onKeyDown={(event) => {
          if (selected && event.key === "Escape" && !event.defaultPrevented) {
            event.stopPropagation();
            if (!pending) onClose?.();
          }
        }}
      >
        {children}
      </div>
    </InspectorContext.Provider>
  );
}

/** Whether the panel is folded away because the list needs the width. */
export function useInspectorCompact() {
  return useContext(InspectorContext).compact;
}

export function InspectorCollection({ children }: { children: ReactNode }) {
  const { compact, selected } = useContext(InspectorContext);
  return (
    <div
      className="collection-inspector-collection"
      hidden={compact && selected}
      inert={compact && selected}
    >
      {children}
    </div>
  );
}

/** A nonmodal desktop inspector becomes the sole detail pane on small screens. */
export function InspectorPanel({
  id,
  label,
  selectionKey,
  closeLabel,
  backLabel,
  pending = false,
  onClose,
  children,
  empty,
  emptyTitle = "Details",
  footer,
  testId,
}: {
  id: string;
  label: string;
  selectionKey: string;
  closeLabel: string;
  backLabel: string;
  pending?: boolean;
  onClose: () => void;
  children: ReactNode;
  empty: ReactNode;
  /** The heading while nothing is selected. */
  emptyTitle?: string;
  footer?: ReactNode;
  testId: string;
}) {
  const { compact } = useContext(InspectorContext);
  const selected = Boolean(selectionKey);
  const heading = useRef<HTMLHeadingElement>(null);
  const body = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (body.current) body.current.scrollTop = 0;
  }, [selectionKey]);
  useLayoutEffect(() => {
    // The list is hidden in compact mode. Never leave focus inside that pane.
    // Desktop row navigation keeps its focus and can inspect records quickly.
    if (compact && selected && heading.current)
      focusWithoutScroll(heading.current);
  }, [compact, selected]);
  return (
    <aside
      id={id}
      className="collection-inspector-panel"
      aria-label={label}
      data-testid={testId}
      hidden={compact && !selected}
      inert={compact && !selected}
      onKeyDown={(event) => {
        if (selected && event.key === "Escape" && !event.defaultPrevented) {
          event.stopPropagation();
          if (!pending) onClose();
        }
      }}
    >
      <header className="collection-inspector-header">
        {compact && selected && (
          <Button
            size="sm"
            color="tertiary"
            iconLeading={ArrowLeft}
            aria-label={backLabel}
            isDisabled={pending}
            onPress={onClose}
          >
            Back
          </Button>
        )}
        <h2
          ref={heading}
          tabIndex={-1}
          className="vult-type-label text-primary outline-none"
        >
          {selected ? "Details" : emptyTitle}
        </h2>
        {!compact && selected && (
          <CloseButton
            label={closeLabel}
            isDisabled={pending}
            onPress={onClose}
          />
        )}
      </header>
      <div
        ref={body}
        className="collection-inspector-body"
        data-empty={!selected || undefined}
        aria-busy={pending}
      >
        {selected ? children : empty}
      </div>
      {selected && footer && (
        <footer className="collection-inspector-footer">{footer}</footer>
      )}
    </aside>
  );
}
