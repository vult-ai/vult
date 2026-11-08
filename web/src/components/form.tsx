import {
  ErrorNotice,
  FieldError,
  type ErrorNoticeProps,
} from "./ui/error-notice";
import { errorMessage } from "../lib/errors";
import {
  cloneElement,
  isValidElement,
  useId,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import type { Currency } from "../lib/api";
import { NativeFieldControl } from "./ui/native-field-control";
import { HintText } from "../design-system/catalog/components/base/input/hint-text";
export function Field({
  label,
  children,
  hint,
  error,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
  error?: string;
}) {
  const id = useId();
  // Select owns its hint ID and aria-describedby association.
  const selectHint =
    isValidElement(children) && children.type === "select" ? hint : undefined;
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {isValidElement(children) ? (
        <NativeFieldControl hint={selectHint}>
          {cloneElement(children as ReactElement<Record<string, unknown>>, {
            id,
            "aria-label": label,
            "aria-describedby":
              [hint && `${id}-hint`, error && `${id}-error`]
                .filter(Boolean)
                .join(" ") || undefined,
            "aria-invalid": error ? true : undefined,
          })}
        </NativeFieldControl>
      ) : (
        children
      )}
      {hint && !selectHint && <HintText id={`${id}-hint`}>{hint}</HintText>}
      <FieldError id={`${id}-error`} error={error} />
    </div>
  );
}
/** Compatibility bridge: every existing form gets the canonical feedback UI. */
export function ErrorMessage({
  message,
  ...props
}: Omit<ErrorNoticeProps, "error"> & { message?: unknown }) {
  return <ErrorNotice error={message} {...props} />;
}
export function message(error: unknown) {
  return errorMessage(error);
}
