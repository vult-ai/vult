import type { ComponentProps } from "react";
import { cx } from "@vult/utils/cx";

export function Kbd({
  className,
  variant = "default",
  ...props
}: ComponentProps<"kbd"> & { variant?: "default" | "plain" }) {
  return (
    <kbd
      {...props}
      className={cx(
        "vult-kbd vult-type-metadata",
        variant === "plain" && "vult-kbd-plain",
        className,
      )}
    />
  );
}
