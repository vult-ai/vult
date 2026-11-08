"use client";

import type { ComponentProps } from "react";
import { NativeSelect as BaseNativeSelect } from "@vult/components/base/select/select-native";
import { cx } from "@vult/utils/cx";

export const NativeSelect = ({
  size = "md",
  selectClassName,
  ...props
}: ComponentProps<typeof BaseNativeSelect>) => (
  <BaseNativeSelect
    {...props}
    size={size}
    data-control-size={size}
    selectClassName={cx("vult-input-control vult-type-ui", selectClassName)}
  />
);
