import {
  useCallback,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactElement,
  type SelectHTMLAttributes,
} from "react";
import { UNSAFE_PortalProvider } from "react-aria";
import { Select as SourceSelect } from "../../design-system/catalog/components/base/select/select";
import { HintText } from "../../design-system/catalog/components/base/input/hint-text";
import { Label } from "../../design-system/catalog/components/base/input/label";
import { cn } from "../../lib/utils";

export interface SelectProps extends Omit<
  SelectHTMLAttributes<HTMLSelectElement>,
  "size"
> {
  label?: string;
  hint?: string;
  selectClassName?: string;
  size?: "sm" | "md" | "lg";
  variant?: "default" | "inline";
  /** `icon` leads the option in the menu and on the trigger, e.g. a colour key. */
  options: readonly { label: string; value: string; disabled?: boolean; icon?: ReactElement }[];
}

/** The shared component owns presentation, positioning, keyboard navigation and motion. The
 * stable native control owns FormData, validation, reset and real change events. */
export function Select({
  id: providedId,
  label,
  hint,
  options,
  className,
  selectClassName,
  size = "md",
  variant = "default",
  value,
  defaultValue,
  disabled,
  required,
  onChange,
  onInvalid,
  onFocus,
  autoFocus,
  ...nativeProps
}: SelectProps) {
  const generatedId = useId();
  const id = providedId ?? `select-${generatedId}`;
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const native = useRef<HTMLSelectElement>(null);
  const surface = useRef<HTMLDivElement>(null);
  const resetting = useRef(false);
  const restoreFrame = useRef<number | undefined>(undefined);
  const [uncontrolled, setUncontrolled] = useState(() =>
    String(defaultValue ?? options[0]?.value ?? ""),
  );
  const [validationError, setValidationError] = useState("");
  const selected = value !== undefined ? String(value) : uncontrolled;
  const focusTrigger = useCallback(
    () =>
      surface.current
        ?.querySelector<HTMLButtonElement>("button")
        ?.focus({ preventScroll: true }),
    [],
  );
  const getContainer = useCallback(
    () =>
      surface.current?.closest<HTMLElement>(
        '[role="dialog"][data-state="open"]',
      ) ?? document.body,
    [],
  );
  useLayoutEffect(
    () => () => {
      if (restoreFrame.current !== undefined)
        cancelAnimationFrame(restoreFrame.current);
    },
    [],
  );

  useLayoutEffect(() => {
    if (autoFocus) focusTrigger();
  }, [autoFocus, focusTrigger]);
  useLayoutEffect(() => {
    const control = native.current;
    const form = control?.form;
    if (!control || !form) return;
    const reset = (event: Event) => {
      // React Aria also observes reset. Only the native control supplies the
      // restored value; its visual counterpart must not emit a second change.
      resetting.current = true;
      queueMicrotask(() => {
        if (!event.defaultPrevented) {
          if (value === undefined) setUncontrolled(control.value);
          else control.value = String(value);
          setValidationError("");
        }
        resetting.current = false;
      });
    };
    form.addEventListener("reset", reset, true);
    return () => form.removeEventListener("reset", reset, true);
  }, [value, nativeProps.form]);

  const description =
    [
      hint ? hintId : nativeProps["aria-describedby"],
      validationError ? errorId : undefined,
    ]
      .filter(Boolean)
      .join(" ") || undefined;
  const items = options.map((option) => ({
    id: option.value,
    label: option.label,
    isDisabled: option.disabled,
    ...(option.icon ? { icon: option.icon } : {}),
  }));
  const placeholder = options.find(
    (option) => option.value === "" && option.disabled,
  )?.label;

  return (
    <div
      className={cn("relative w-full min-w-0", className)}
      title={nativeProps.title}
    >
      {label && (
        <Label htmlFor={id} className="mb-1.5">
          {label}
        </Label>
      )}
      <select
        {...nativeProps}
        ref={native}
        id={`${id}-native`}
        className="sr-only"
        tabIndex={-1}
        aria-hidden="true"
        aria-describedby={description}
        value={value}
        defaultValue={defaultValue}
        disabled={disabled}
        required={required}
        onFocus={(event) => {
          focusTrigger();
          onFocus?.(event);
        }}
        onInvalid={(event) => {
          onInvalid?.(event);
          if (event.defaultPrevented) return;
          event.preventDefault();
          setValidationError(event.currentTarget.validationMessage);
          // Native validation still blocks submission; focus its visible control.
          const firstInvalid = Array.from(
            event.currentTarget.form?.elements ?? [],
          ).find(
            (element) =>
              "validity" in element &&
              !(element as HTMLInputElement).validity.valid,
          );
          if (firstInvalid === event.currentTarget) focusTrigger();
        }}
        onChange={(event) => {
          if (value === undefined) setUncontrolled(event.currentTarget.value);
          setValidationError("");
          onChange?.(event);
        }}
      >
        {options.map((option) => (
          <option
            key={option.value}
            value={option.value}
            disabled={option.disabled}
          >
            {option.label}
          </option>
        ))}
      </select>
      <UNSAFE_PortalProvider getContainer={getContainer}>
        <SourceSelect
          ref={surface}
          id={id}
          aria-label={nativeProps["aria-label"] ?? label}
          aria-labelledby={nativeProps["aria-labelledby"]}
          aria-describedby={description}
          className={selectClassName}
          size={size}
          variant={variant}
          isDisabled={disabled}
          isRequired={required}
          isInvalid={
            !!validationError ||
            nativeProps["aria-invalid"] === true ||
            nativeProps["aria-invalid"] === "true"
          }
          validationBehavior="aria"
          placeholder={placeholder}
          items={items}
          onOpenChange={(open) => {
            if (restoreFrame.current !== undefined)
              cancelAnimationFrame(restoreFrame.current);
            if (open) return;
            const dialog = surface.current?.closest<HTMLElement>(
              '[role="dialog"][data-state="open"]',
            );
            if (!dialog) return;
            restoreFrame.current = requestAnimationFrame(() => {
              // Radix can focus its frame when the focused option is removed.
              // Restore this selector only if no other field received focus.
              if (
                dialog.dataset.state === "open" &&
                (document.activeElement === dialog ||
                  document.activeElement === document.body)
              )
                focusTrigger();
            });
          }}
          value={placeholder && selected === "" ? null : selected}
          onChange={(key) => {
            const control = native.current;
            const next = String(key ?? "");
            if (
              !control ||
              resetting.current ||
              control.matches(":disabled") ||
              next === control.value ||
              options.find((option) => option.value === next)?.disabled
            )
              return;
            control.value = next;
            control.dispatchEvent(new Event("input", { bubbles: true }));
            control.dispatchEvent(new Event("change", { bubbles: true }));
          }}
        >
          {(item) => <SourceSelect.Item {...item} />}
        </SourceSelect>
      </UNSAFE_PortalProvider>
      {hint && (
        <HintText id={hintId} className="mt-2">
          {hint}
        </HintText>
      )}
      {validationError && (
        <HintText id={errorId} isInvalid className="mt-2">
          {validationError}
        </HintText>
      )}
    </div>
  );
}
