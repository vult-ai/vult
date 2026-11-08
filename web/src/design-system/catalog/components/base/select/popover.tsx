"use client";

import { useCallback, type RefAttributes } from "react";
import { useObjectRef } from "@react-aria/utils";
import type { PopoverProps as AriaPopoverProps } from "react-aria-components";
import { Popover as AriaPopover } from "react-aria-components";
import { cx } from "@vult/utils/cx";

interface PopoverProps extends AriaPopoverProps, RefAttributes<HTMLElement> {
  size: "sm" | "md" | "lg";
  alignToSelectedItem?: boolean;
}

export const Popover = ({
  size,
  alignToSelectedItem = false,
  ref: forwardedRef,
  className,
  ...props
}: PopoverProps) => {
  const ref = useObjectRef(forwardedRef);
  const getTargetRect = useCallback(
    (target: Element) => {
      const trigger = target.getBoundingClientRect();
      const menu = ref.current;
      const item =
        menu?.querySelector<HTMLElement>(
          '[role="option"][aria-selected="true"]',
        ) ??
        menu?.querySelector<HTMLElement>(
          '[role="option"]:not([aria-disabled="true"])',
        );
      if (!menu || !item) return trigger;

      const menuRect = menu.getBoundingClientRect();
      const itemRect = item.getBoundingClientRect();
      const viewport = target.ownerDocument.defaultView?.visualViewport;
      const viewportTop = (viewport?.offsetTop ?? 0) + 8;
      const viewportBottom =
        (viewport?.offsetTop ?? 0) +
        (viewport?.height ??
          target.ownerDocument.documentElement.clientHeight) -
        8;
      const triggerCenter = trigger.top + trigger.height / 2;
      const itemCenter =
        itemRect.top - menuRect.top + menu.scrollTop + itemRect.height / 2;
      const visibleItemCenter = Math.min(
        itemCenter,
        menuRect.height - itemRect.height / 2 - 4,
      );
      const top = Math.max(
        viewportTop,
        Math.min(
          triggerCenter - visibleItemCenter,
          viewportBottom - menuRect.height,
        ),
      );

      // Long menus scroll to the selection; short menus grow around it. Only
      // positioning changes here—React Aria still owns focus and selection.
      menu.scrollTop = Math.max(
        0,
        Math.min(
          itemCenter - (triggerCenter - top),
          menu.scrollHeight - menu.clientHeight,
        ),
      );
      return new DOMRect(trigger.left, top, trigger.width, 0);
    },
    [ref],
  );

  return (
    <AriaPopover
      placement={alignToSelectedItem ? "bottom start" : "bottom"}
      containerPadding={alignToSelectedItem ? 8 : 0}
      offset={alignToSelectedItem ? 0 : 4}
      shouldFlip={!alignToSelectedItem}
      getTargetRect={alignToSelectedItem ? getTargetRect : undefined}
      shouldSkipAnimation={alignToSelectedItem}
      {...props}
      ref={ref}
      data-control-size={size}
      data-menu-kind="select"
      data-selected-aligned={alignToSelectedItem || undefined}
      className={(state) =>
        cx(
          "vult-menu vult-surface-paper origin-(--trigger-anchor-point) overflow-x-hidden overflow-y-auto rounded-lg shadow-lg ring-1 ring-secondary_alt outline-hidden will-change-transform",

          !alignToSelectedItem &&
            state.isEntering &&
            "duration-150 ease-out animate-in fade-in placement-right:slide-in-from-left-0.5 placement-top:slide-in-from-bottom-0.5 placement-bottom:slide-in-from-top-0.5",
          !alignToSelectedItem &&
            state.isExiting &&
            "duration-100 ease-in animate-out fade-out placement-right:slide-out-to-left-0.5 placement-top:slide-out-to-bottom-0.5 placement-bottom:slide-out-to-top-0.5",


          typeof className === "function" ? className(state) : className,
        )
      }
    />
  );
};
