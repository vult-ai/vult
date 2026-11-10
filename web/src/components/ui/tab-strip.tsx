import { useContext, useLayoutEffect, useRef, type ReactNode } from "react";
import { Tab, TabList, TabListStateContext } from "react-aria-components";
import "./tab-strip.css";

export type TabStripItem = {
  id: string;
  label: ReactNode;
  isDisabled?: boolean;
};

/** Tab standard v1 (docs/design/tabs.md): the one tab strip. The parent Tabs
 * owns panels and selection; screens never restyle the strip or its tabs. */
export function TabStrip({
  items,
  "aria-label": ariaLabel,
}: {
  items: readonly TabStripItem[];
  "aria-label": string;
}) {
  const state = useContext(TabListStateContext);
  const strip = useRef<HTMLDivElement>(null);
  const indicator = useRef<HTMLSpanElement>(null);
  const animation = useRef<Animation | undefined>(undefined);
  const target = useRef<
    { key: unknown; left: number; width: number } | undefined
  >(undefined);
  const keys = items.map((item) => item.id).join(" ");
  useLayoutEffect(() => {
    const list = strip.current;
    const marker = indicator.current;
    if (!list || !marker) return;
    const measure = () => {
      const selected = list.querySelector<HTMLElement>(
        '[role="tab"][aria-selected="true"]',
      );
      if (!selected) {
        marker.removeAttribute("data-positioned");
        return;
      }
      const bounds = selected.getBoundingClientRect();
      if (!bounds.width) return;
      const frame = list.getBoundingClientRect();
      const next = {
        key: state?.selectedKey,
        left: bounds.left - frame.left,
        width: bounds.width,
      };
      const previous = target.current;
      target.current = next;
      if (
        previous &&
        Math.abs(previous.left - next.left) < 0.1 &&
        Math.abs(previous.width - next.width) < 0.1
      )
        return;
      // Capture the visible position before cancelling an interrupted move.
      const painted = marker.getBoundingClientRect();
      animation.current?.cancel();
      marker.style.width = `${next.width}px`;
      marker.style.transform = `translateX(${next.left}px)`;
      marker.setAttribute("data-positioned", "true");
      const keyboard =
        selected.hasAttribute("data-focus-visible") ||
        document.documentElement.dataset.vultFocusVisible === "true";
      if (
        !previous ||
        previous.key === next.key ||
        keyboard ||
        window.matchMedia("(prefers-reduced-motion: reduce)").matches ||
        typeof marker.animate !== "function"
      )
        return;
      const tokens = getComputedStyle(list);
      const duration = tokens.getPropertyValue("--vult-motion-spatial").trim();
      animation.current = marker.animate(
        [
          {
            transform: `translateX(${painted.left - frame.left}px) scaleX(${painted.width / next.width})`,
          },
          { transform: `translateX(${next.left}px) scaleX(1)` },
        ],
        {
          duration:
            (parseFloat(duration) || 165) *
            (duration.endsWith("s") && !duration.endsWith("ms") ? 1000 : 1),
          easing:
            tokens.getPropertyValue("--vult-ease-settle").trim() ||
            "cubic-bezier(0.23, 1, 0.32, 1)",
        },
      );
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(list);
    for (const tab of list.querySelectorAll('[role="tab"]'))
      observer.observe(tab);
    return () => observer.disconnect();
  }, [state?.selectedKey, keys]);
  useLayoutEffect(() => () => animation.current?.cancel(), []);
  // A strip wider than its space scrolls. The side with more tabs past it
  // fades, and the selected tab is always scrolled fully into view.
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const box = scroller.current;
    if (!box) return;
    const edges = () => {
      const end = box.scrollWidth - box.clientWidth;
      box.toggleAttribute("data-more-start", box.scrollLeft > 1);
      box.toggleAttribute("data-more-end", box.scrollLeft < end - 1);
    };
    const selected = box.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    if (selected) {
      const left = selected.offsetLeft - 24;
      const right = selected.offsetLeft + selected.offsetWidth + 24 - box.clientWidth;
      if (box.scrollLeft > left) box.scrollLeft = Math.max(0, left);
      else if (box.scrollLeft < right) box.scrollLeft = right;
    }
    edges();
    box.addEventListener("scroll", edges, { passive: true });
    const observer = new ResizeObserver(edges);
    observer.observe(box);
    return () => {
      box.removeEventListener("scroll", edges);
      observer.disconnect();
    };
  }, [state?.selectedKey, keys]);

  return (
    <div ref={scroller} className="vult-tab-strip-scroll">
      <TabList
        ref={strip}
        aria-label={ariaLabel}
        className="vult-tab-strip"
        render={(props) => (
          <div {...props}>
            <span
              ref={indicator}
              className="vult-tab-strip-indicator"
              aria-hidden="true"
            />
            {props.children}
          </div>
        )}
      >
        {items.map((item) => (
          <Tab
            key={item.id}
            id={item.id}
            isDisabled={item.isDisabled}
            className="vult-tab-strip-item vult-type-label"
          >
            {item.label}
          </Tab>
        ))}
      </TabList>
    </div>
  );
}
