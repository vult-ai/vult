import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type RefObject,
  type PointerEvent,
  type KeyboardEvent,
} from "react";

type Session = {
  context: string;
  id: string;
  label: string;
  ids: string[];
  index: number;
  started: boolean;
  button: HTMLButtonElement;
  row: HTMLElement;
  rows: HTMLElement[];
  positions: Map<HTMLElement, number>;
  pointerId?: number;
  capture?: HTMLElement;
  y: number;
  startY: number;
  scrollTop: number;
  centers: number[];
  contentHeight: number;
  minCenter: number;
  maxCenter: number;
  height: number;
  frameTime?: number;
  offset: number;
};

/** Pointer/touch and keyboard share a preview and commit boundary. */
export function useRowOrder({
  ids,
  scope,
  scroll,
  onCommit,
}: {
  ids: readonly string[];
  scope: string;
  scroll: RefObject<HTMLElement | null>;
  onCommit: (moved: string, position: number) => void;
}) {
  const instructionsId = useId();
  const current = useRef<Session | null>(null);
  const frame = useRef(0);
  const animations = useRef(new Map<HTMLElement, Animation>());
  const release = useRef<{
    context: string;
    row: HTMLElement;
    center: number;
  } | null>(null);
  const [preview, setPreview] = useState<Session | null>(null);
  const [announcement, announce] = useState("");
  const signature = JSON.stringify(ids);
  const context = `${scope}:${signature}`;
  const activeContext = useRef(context);
  activeContext.current = context;
  const reducedMotion = () =>
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const measure = (rows: HTMLElement[]) =>
    new Map(rows.map((row) => [row, row.getBoundingClientRect().top]));
  const keepVisible = (row: HTMLElement) => {
    const element = scroll.current;
    if (!element) return;
    const pane = element.getBoundingClientRect();
    const bounds = row.getBoundingClientRect();
    if (bounds.top < pane.top) element.scrollTop -= pane.top - bounds.top;
    else if (bounds.bottom > pane.bottom)
      element.scrollTop += bounds.bottom - pane.bottom;
  };
  const animate = (row: HTMLElement, offset: number) => {
    animations.current.get(row)?.cancel();
    animations.current.delete(row);
    if (reducedMotion() || Math.abs(offset) < 0.5 || !row.animate) return;
    const animation = row.animate(
      [
        { transform: `translate3d(0, ${offset}px, 0)` },
        { transform: "translate3d(0, 0, 0)" },
      ],
      { duration: 160, easing: "cubic-bezier(0.2, 0.8, 0.2, 1)" },
    );
    animations.current.set(row, animation);
    animation.onfinish = () => {
      if (animations.current.get(row) === animation)
        animations.current.delete(row);
    };
  };
  const clear = () => {
    const session = current.current;
    // Release capture only after retiring the session: the resulting lost event
    // must not cancel a finished drop.
    current.current = null;
    if (
      session?.pointerId !== undefined &&
      session.capture?.hasPointerCapture(session.pointerId)
    )
      session.capture.releasePointerCapture(session.pointerId);
    if (session) session.row.style.removeProperty("transform");
    animations.current.forEach((animation) => animation.cancel());
    animations.current.clear();
    cancelAnimationFrame(frame.current);
  };
  const stop = () => {
    const session = current.current;
    if (session?.started && session.pointerId !== undefined) {
      const bounds = session.row.getBoundingClientRect();
      release.current = {
        context: session.context,
        row: session.row,
        center: bounds.y + bounds.height / 2,
      };
    }
    clear();
    setPreview(null);
  };
  const movedIds = (session: Session) => {
    const next = session.ids.filter((id) => id !== session.id);
    next.splice(session.index, 0, session.id);
    return next;
  };
  const targetCenter = (session: Session) => {
    const element = scroll.current;
    if (!element) return session.centers[session.ids.indexOf(session.id)];
    const bounds = element.getBoundingClientRect();
    const half = session.height / 2;
    const desired =
      session.centers[session.ids.indexOf(session.id)] +
      session.y -
      session.startY;
    // Keep the held row visible while the pointer rests at or outside an edge.
    const visible = Math.max(
      bounds.top + half,
      Math.min(bounds.bottom - half, desired),
    );
    const content = visible + element.scrollTop - session.scrollTop;
    return Math.max(session.minCenter, Math.min(session.maxCenter, content));
  };
  // Functional movement follows the pointer directly, with no spring lag.
  const paintDrag = () => {
    const session = current.current;
    if (
      !session?.started ||
      session.pointerId === undefined ||
      !session.row.isConnected
    )
      return;
    const bounds = session.row.getBoundingClientRect();
    const baseCenter = bounds.y + bounds.height / 2 - session.offset;
    const target =
      targetCenter(session) -
      (scroll.current?.scrollTop || 0) +
      session.scrollTop;
    session.offset = target - baseCenter;
    session.row.style.transform = `translate3d(0, ${session.offset}px, 0)`;
  };
  const update = (index: number) => {
    const session = current.current;
    if (!session) return;
    const next = {
      ...session,
      index: Math.max(0, Math.min(session.ids.length - 1, index)),
      started: true,
      positions: measure(session.rows),
    };
    current.current = next;
    setPreview(next);
    announce(
      `${next.label}, position ${next.index + 1} of ${next.ids.length}.`,
    );
  };
  const finish = () => {
    const session = current.current;
    stop();
    if (
      !session?.started ||
      session.context !== activeContext.current ||
      session.index === session.ids.indexOf(session.id)
    )
      return;
    onCommit(session.id, session.index);
  };
  const cancel = () => {
    if (current.current)
      announce(`Reordering ${current.current.label} cancelled.`);
    stop();
  };
  const followPointer = () => {
    const session = current.current;
    if (!session?.started || session.pointerId === undefined) return;
    const y = targetCenter(session);
    let index = 0;
    for (let i = 1; i < session.centers.length; i++)
      if (
        Math.abs(session.centers[i] - y) < Math.abs(session.centers[index] - y)
      )
        index = i;
    if (session.index !== index) update(index);
    paintDrag();
  };
  const autoScroll = (timestamp: number) => {
    const session = current.current;
    const element = scroll.current;
    if (!session?.started || !element || session.pointerId === undefined)
      return;
    const bounds = element.getBoundingClientRect();
    const elapsed = Math.min(
      32,
      Math.max(0, timestamp - (session.frameTime ?? timestamp - 1000 / 60)),
    );
    session.frameTime = timestamp;
    const edge = Math.min(64, element.clientHeight / 4);
    const proximity =
      session.y < bounds.top + edge
        ? -Math.min(1, (bounds.top + edge - session.y) / edge)
        : session.y > bounds.bottom - edge
          ? Math.min(1, (session.y - bounds.bottom + edge) / edge)
          : 0;
    // Transformed children can enlarge scrollHeight. Use the measured normal
    // list extent, never the overflow generated by the row being dragged.
    const limit = Math.max(0, session.contentHeight - element.clientHeight);
    const next = Math.max(
      0,
      Math.min(limit, element.scrollTop + (proximity * 900 * elapsed) / 1000),
    );
    if (next !== element.scrollTop) element.scrollTop = next;
    followPointer();
    frame.current = requestAnimationFrame(autoScroll);
  };
  useEffect(() => {
    if (current.current) cancel();
    const blur = () => cancel();
    window.addEventListener("blur", blur);
    return () => {
      if (current.current) clear();
      window.removeEventListener("blur", blur);
    };
  }, [scope, signature]);
  useEffect(
    () => () => {
      release.current = null;
      clear();
    },
    [],
  );
  useLayoutEffect(() => {
    if (preview?.started && preview.context === context) {
      if (preview.pointerId === undefined) {
        preview.button.focus({ preventScroll: true });
        keepVisible(preview.row);
      } else {
        // FLIP only displaced neighbors; the dragged row stays under the cursor.
        for (const row of preview.rows) {
          if (row === preview.row || !row.isConnected) continue;
          const previous = preview.positions.get(row);
          animations.current.get(row)?.cancel();
          if (previous !== undefined)
            animate(row, previous - row.getBoundingClientRect().top);
        }
        paintDrag();
      }
    } else if (release.current) {
      const pending = release.current;
      release.current = null;
      if (pending.row.isConnected) {
        keepVisible(pending.row);
        const bounds = pending.row.getBoundingClientRect();
        animate(pending.row, pending.center - bounds.y - bounds.height / 2);
      }
    }
  }, [preview, context]);

  return {
    instructionsId,
    announcement,
    previewIds:
      preview?.started && preview.context === context
        ? movedIds(preview)
        : null,
    dragging:
      preview?.started && preview.context === context ? preview.id : null,
    pointerDragging:
      preview?.started &&
      preview.pointerId !== undefined &&
      preview.context === context
        ? preview.id
        : null,
    container: {
      onPointerMove: (event: PointerEvent<HTMLElement>) => {
        const session = current.current;
        if (!session || session.pointerId !== event.pointerId) return;
        session.y = event.clientY;
        if (!session.started && Math.abs(event.clientY - session.startY) >= 4) {
          update(session.index);
          frame.current = requestAnimationFrame(autoScroll);
        }
        followPointer();
      },
      onPointerUp: (event: PointerEvent<HTMLElement>) => {
        if (current.current?.pointerId === event.pointerId) finish();
      },
      onPointerCancel: (event: PointerEvent<HTMLElement>) => {
        if (current.current?.pointerId === event.pointerId) cancel();
      },
      onLostPointerCapture: (event: PointerEvent<HTMLElement>) => {
        const session = current.current;
        // Ignore capture transferred from a touch handle to the stable owner.
        if (
          session?.pointerId === event.pointerId &&
          event.target === session.capture
        )
          cancel();
      },
    },
    handle: (id: string, label: string) => ({
      type: "button" as const,
      "aria-label": `Reorder ${label}`,
      "aria-describedby": instructionsId,
      "aria-pressed": (preview?.id === id && preview.started) || false,
      title:
        "Drag to reorder. Or press Space, use arrow keys, then Enter to save.",
      onPointerDown: (event: PointerEvent<HTMLButtonElement>) => {
        if (
          !event.isPrimary ||
          event.button !== 0 ||
          current.current ||
          ids.length < 2
        )
          return;
        const rows = [
          ...(scroll.current?.querySelectorAll<HTMLElement>(
            "[data-account-code]",
          ) || []),
        ];
        const row = event.currentTarget.closest<HTMLElement>(
          "[data-account-code]",
        );
        const capture = scroll.current;
        if (!row || !capture || rows.length !== ids.length) return;
        // Pointer dragging must not trigger keyboard focus decoration or selection.
        event.preventDefault();
        animations.current.forEach((animation) => animation.cancel());
        animations.current.clear();
        const rowBounds = row.getBoundingClientRect();
        const first = rows[0].getBoundingClientRect();
        const last = rows[rows.length - 1].getBoundingClientRect();
        const listBounds = row.parentElement?.getBoundingClientRect();
        const captureBounds = capture.getBoundingClientRect();
        const padding =
          parseFloat(getComputedStyle(capture).paddingBottom) || 0;
        const naturalList = listBounds && listBounds.height > 0;
        // This container never moves when React reorders its rows. Capturing on
        // the handle itself loses the grab as soon as its row changes position.
        capture.setPointerCapture(event.pointerId);
        current.current = {
          context,
          id,
          label,
          ids: [...ids],
          index: ids.indexOf(id),
          started: false,
          button: event.currentTarget,
          row,
          rows,
          positions: measure(rows),
          pointerId: event.pointerId,
          capture,
          y: event.clientY,
          startY: event.clientY,
          scrollTop: scroll.current?.scrollTop || 0,
          offset: 0,
          height: rowBounds.height,
          minCenter:
            (naturalList ? listBounds.top : first.y) + rowBounds.height / 2,
          maxCenter:
            (naturalList ? listBounds.bottom : last.y + last.height) -
            rowBounds.height / 2,
          contentHeight: naturalList
            ? listBounds.bottom -
              captureBounds.top -
              capture.clientTop +
              capture.scrollTop +
              padding
            : capture.scrollHeight,
          centers: rows.map((row) => {
            const r = row.getBoundingClientRect();
            return r.y + r.height / 2;
          }),
        };
      },
      onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => {
        if ([" ", "Enter"].includes(event.key)) {
          event.preventDefault();
          if (current.current?.id === id) finish();
          else if (!current.current && ids.length > 1) {
            const row = event.currentTarget.closest<HTMLElement>(
              "[data-account-code]",
            )!;
            const rows = [
              ...(scroll.current?.querySelectorAll<HTMLElement>(
                "[data-account-code]",
              ) || []),
            ];
            current.current = {
              context,
              id,
              label,
              ids: [...ids],
              index: ids.indexOf(id),
              started: true,
              button: event.currentTarget,
              row,
              rows,
              positions: new Map(),
              y: 0,
              startY: 0,
              scrollTop: 0,
              centers: [],
              contentHeight: 0,
              minCenter: 0,
              maxCenter: 0,
              height: 0,
              offset: 0,
            };
            update(ids.indexOf(id));
          }
        } else if (current.current?.id === id) {
          const session = current.current;
          if (event.key === "Tab") finish();
          else if (event.key === "Escape") {
            event.preventDefault();
            cancel();
          } else if (
            ["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)
          ) {
            event.preventDefault();
            update(
              event.key === "Home"
                ? 0
                : event.key === "End"
                  ? ids.length - 1
                  : session.index + (event.key === "ArrowUp" ? -1 : 1),
            );
          }
        }
      },
    }),
  };
}
