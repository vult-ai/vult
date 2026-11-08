"use client";

import { Button as BaseButton } from "@vult/components/base/buttons/button";
import type {
  ButtonProps,
  CommonProps,
  Props,
} from "@vult/components/base/buttons/button";

export type { ButtonProps, CommonProps, Props };
export { styles } from "@vult/components/base/buttons/button";

export const Button = ({ size = "md", ...props }: Props) => (
  <BaseButton {...props} size={size} />
);
