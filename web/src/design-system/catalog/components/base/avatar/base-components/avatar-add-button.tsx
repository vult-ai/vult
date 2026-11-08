"use client";

import { Plus } from "../../../../../icons";
import type { ButtonProps as AriaButtonProps } from "react-aria-components";
import {
  Tooltip as AriaTooltip,
  TooltipTrigger as AriaTooltipTrigger,
} from "@vult/components/base/tooltip/tooltip";
import { cx } from "@vult/utils/cx";

const sizes = {
  xs: { root: "size-6", icon: "size-4" },
  sm: { root: "size-8", icon: "size-4" },
  md: { root: "size-10", icon: "size-5" },
};

interface AvatarAddButtonProps extends AriaButtonProps {
  size: "xs" | "sm" | "md";
  title?: string;
  className?: string;
}

export const AvatarAddButton = ({
  size,
  className,
  title = "Add user",
  ...props
}: AvatarAddButtonProps) => (
  <AriaTooltip title={title}>
    <AriaTooltipTrigger
      {...props}
      aria-label={title}
      className={cx(
        "flex cursor-pointer items-center justify-center rounded-full border border-dashed border-primary bg-primary text-fg-quaternary outline-focus-ring transition duration-100 ease-linear hover:bg-primary_hover hover:text-fg-quaternary_hover disabled:opacity-50 vult-focus-target",
        sizes[size].root,
        className,
      )}
    >
      <Plus
        className={cx("text-current transition-inherit-all", sizes[size].icon)}
      />
    </AriaTooltipTrigger>
  </AriaTooltip>
);
