// Native event adapter for Vult's canonical controls and action lifecycle.
import { useLayoutEffect, useRef, type ButtonHTMLAttributes } from "react";
import { Loading01 as LoaderCircle } from "../../design-system/icons";
import { cn } from "../../lib/utils";
import { useButtonAction } from "../../design-system/use-button-action";

// Button standard v1 (docs/design/buttons.md): the same colours and sizes as
// the catalog Button, which menus, popovers and links use.
const colors = {
  primary: "primary",
  secondary: "secondary",
  tertiary: "ghost",
  link: "link",
  /** Only inside the shared confirmation; see docs/design/feedback.md. */
  destructive: "destructive",
} as const;
export type ButtonColor = keyof typeof colors;
export type ButtonSize = "sm" | "md" | "lg";

/** An action button: forms, saves and other steps that run and finish. */
export function Button({
  className,
  color = "primary",
  size = "md",
  type = "button",
  disabled,
  busy = false,
  onClick,
  onClickCapture,
  children,
  ...props
}: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "color"> & {
  color?: ButtonColor;
  size?: ButtonSize;
  busy?: boolean;
}) {
  const action = useButtonAction(onClick, disabled || busy, type);
  const pending = busy || action.pending;
  const ref = useRef<HTMLButtonElement>(null);
  const label = useRef("");
  const width = useRef(0);
  useLayoutEffect(() => {
    if (!pending && ref.current) {
      label.current = ref.current.textContent ?? "";
      width.current = ref.current.getBoundingClientRect().width;
    }
  });
  return (
    <button
      ref={ref}
      type={type}
      className={cn(
        "vult-button",
        `vult-button-${colors[color]}`,
        size === "sm"
          ? "vult-button-sm"
          : size === "lg"
            ? "vult-button-lg"
            : "vult-button-md",
        className,
      )}
      {...props}
      disabled={disabled || pending}
      data-disabled={disabled || pending || undefined}
      aria-busy={pending || undefined}
      aria-label={props["aria-label"] ?? (pending ? label.current : undefined)}
      data-loading={pending || undefined}
      style={{
        ...props.style,
        ...(pending && width.current
          ? { width: width.current, maxWidth: "100%" }
          : {}),
      }}
      onClick={action.onPress}
      onClickCapture={(event) => {
        onClickCapture?.(event);
        if (!event.defaultPrevented) action.onClickCapture?.(event);
      }}
    >
      <span className="vult-button-copy">{children}</span>
      {pending ? (
        <LoaderCircle aria-hidden="true" className="vult-button-loading" />
      ) : null}
    </button>
  );
}
