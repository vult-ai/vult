"use client";

import type { ComponentProps } from "react";
import { Dropdown as BaseDropdown } from "@vult/components/base/dropdown/dropdown";
import { cx } from "@vult/utils/cx";

type DropdownItemProps = ComponentProps<typeof BaseDropdown.Item>;
type DropdownPopoverProps = Omit<
  ComponentProps<typeof BaseDropdown.Popover>,
  "className"
> & {
  className?: string;
  appearance?: "default" | "control";
};

const DropdownItem = (props: DropdownItemProps) => (
  <BaseDropdown.Item {...props} />
);

const DropdownPopover = ({
  className,
  offset = 12,
  appearance = "default",
  ...props
}: DropdownPopoverProps) => (
  <BaseDropdown.Popover
    {...props}
    {...(appearance === "control" ? { "data-vult-surface": undefined } : {})}
    offset={offset}
    className={cx(
      appearance === "control"
        ? "vult-option-menu vult-surface-paper rounded-lg shadow-lg ring-secondary_alt"
        : "vult-dropdown-popover rounded-lg shadow-md ring-secondary",
      className,
    )}
  />
);

export const Dropdown = {
  ...BaseDropdown,
  Item: DropdownItem,
  Popover: DropdownPopover,
};
