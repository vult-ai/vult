import { useEffect, useMemo, useRef, useState } from "react";
import { Tooltip } from "../../design-system/catalog/components/base/tooltip/tooltip";

/** One retained tooltip follows the collapsed rail's native navigation controls. */
export function useRailTooltip(expanded: boolean) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const anchorRef = useMemo(() => ({ current: anchor }), [anchor]);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancel = () => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
  };
  const close = () => {
    cancel();
    setAnchor(null);
  };
  const dismiss = () => {
    cancel();
    timer.current = setTimeout(() => setAnchor(null), 140);
  };
  const show = (target: EventTarget | null, immediate = false) => {
    if (
      expanded ||
      !matchMedia("(min-width: 768px)").matches ||
      !(target instanceof Element)
    )
      return;
    const control = target.closest<HTMLElement>(
      "a[aria-label], button[aria-label]",
    );
    if (!control || control.closest("[inert]")) return;
    cancel();
    if (anchor || immediate) setAnchor(control);
    else timer.current = setTimeout(() => setAnchor(control), 100);
  };
  useEffect(() => {
    if (expanded) close();
    return cancel;
  }, [expanded]);
  useEffect(() => {
    const media = matchMedia("(min-width: 768px)");
    const changed = () => {
      if (!media.matches) close();
    };
    media.addEventListener("change", changed);
    return () => media.removeEventListener("change", changed);
  }, []);
  useEffect(() => {
    if (!anchor) return;
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      close();
    };
    window.addEventListener("keydown", escape, true);
    return () => window.removeEventListener("keydown", escape, true);
  }, [anchor]);
  return {
    handlers: {
      onPointerOver: (event: React.PointerEvent<HTMLElement>) => {
        if (event.pointerType === "mouse") show(event.target);
      },
      onPointerOut: (event: React.PointerEvent<HTMLElement>) => {
        if (event.pointerType !== "mouse") return;
        const next = event.relatedTarget;
        if (
          next instanceof Element &&
          next.closest("a[aria-label], button[aria-label]")
        )
          return;
        dismiss();
      },
      onPointerLeave: dismiss,
      onFocusCapture: (event: React.FocusEvent<HTMLElement>) =>
        show(event.target, true),
      onBlurCapture: dismiss,
      onClickCapture: close,
    },
    tooltip: (
      <Tooltip
        title={anchor?.getAttribute("aria-label") ?? ""}
        triggerRef={anchorRef}
        isOpen={!expanded && Boolean(anchor)}
        onOpenChange={(open) => {
          if (!open) close();
        }}
        placement="right"
        arrow
        offset={12}
        motion="scan"
      >
        {null}
      </Tooltip>
    ),
  };
}
