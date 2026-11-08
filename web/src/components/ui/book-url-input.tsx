import type { InputHTMLAttributes } from "react";
import { cn } from "../../lib/utils";
import { NativeFieldControl } from "./native-field-control";
import "./book-url-input.css";

/** One joined address control for Book creation and later URL edits. */
export function BookURLInput({
  className,
  disabled,
  ...props
}: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <div
      className={cn("book-url-input", className)}
      data-vult-joined
      data-disabled={disabled || undefined}
    >
      <div className="vult-input-group" data-input-size="md">
        <div data-leading>
          <span
            className="vult-input-prefix book-url-host"
            title={`${window.location.origin}/`}
          >
            {window.location.host}/
          </span>
        </div>
        <NativeFieldControl>
          <input
            placeholder="choose-a-name"
            maxLength={63}
            spellCheck={false}
            autoCapitalize="none"
            autoComplete="off"
            {...props}
            disabled={disabled}
          />
        </NativeFieldControl>
      </div>
    </div>
  );
}
