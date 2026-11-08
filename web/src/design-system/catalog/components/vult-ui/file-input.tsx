"use client";

import { type ComponentPropsWithRef, type ReactNode, useId } from "react";
import { HintText } from "@vult/components/base/input/hint-text";
import { Label } from "@vult/components/base/input/label";
import { cx } from "@vult/utils/cx";

export interface FileInputProps extends Omit<
  ComponentPropsWithRef<"input">,
  "type" | "value" | "defaultValue" | "size"
> {
  label?: string;
  hint?: ReactNode;
  size?: "sm" | "md" | "lg";
}

/** File selection is a native event/FileList, never a controlled text value. */
export function FileInput({
  label,
  hint,
  size = "md",
  id,
  className,
  disabled,
  "aria-describedby": describedBy,
  ...props
}: FileInputProps) {
  const generatedId = useId();
  const inputId = id ?? `file-input-${generatedId}`;
  const hintId = `${inputId}-hint`;
  return (
    <div className={cx("flex w-full flex-col gap-1.5", className)}>
      {label ? (
        <Label htmlFor={inputId} isRequired={props.required}>
          {label}
        </Label>
      ) : null}
      <div
        className={cx(
          "vult-input-control vult-focus-within relative flex w-full items-center px-3",
          disabled && "vult-input-disabled",
        )}
        data-control-size={size}
      >
        <input
          {...props}
          id={inputId}
          type="file"
          disabled={disabled}
          aria-describedby={
            [describedBy, hint ? hintId : null].filter(Boolean).join(" ") ||
            undefined
          }
          className="m-0 w-full min-w-0 bg-transparent outline-hidden"
        />
      </div>
      {hint ? <HintText id={hintId}>{hint}</HintText> : null}
    </div>
  );
}
