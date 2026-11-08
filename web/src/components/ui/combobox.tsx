import {
  useCallback,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
  type ReactNode,
} from "react";
import { UNSAFE_PortalProvider } from "react-aria";
import {
  ComboBox as AriaComboBox,
  ComboBoxStateContext,
  ListBox,
} from "react-aria-components";
import { ChevronDown } from "../../design-system/icons";
import { InputBase } from "../../design-system/catalog/components/base/input/input";
import { Label } from "../../design-system/catalog/components/base/input/label";
import { Popover } from "../../design-system/catalog/components/base/select/popover";
import {
  Select,
  SelectContext,
} from "../../design-system/catalog/components/base/select/select";
import { choiceMenuPosition } from "./choice-menu-position";
import { cn } from "../../lib/utils";

export type ComboBoxOption = {
  id: string;
  label: string;
  onAction?: () => void;
};
type ControlSize = "sm" | "md" | "lg";
const menuHeights = { sm: 224, md: 256, lg: 320 };

function ComboBoxInput({
  placeholder,
  size,
  dismissRef,
  triggerRef,
}: {
  placeholder: string;
  size: ControlSize;
  dismissRef: RefObject<() => void>;
  triggerRef: RefObject<HTMLDivElement | null>;
}) {
  const state = useContext(ComboBoxStateContext);
  useLayoutEffect(() => {
    dismissRef.current = () => state?.revert();
    return () => {
      dismissRef.current = () => {};
    };
  }, [state, dismissRef]);
  return (
    <div
      className="relative"
      onClick={(event) => {
        const input = event.currentTarget.querySelector("input");
        if (!input || input.disabled || input.readOnly) return;
        input.focus();
        if (!state?.isOpen) state?.open(null, "manual");
      }}
    >
      <InputBase
        groupRef={triggerRef}
        size={size}
        placeholder={placeholder}
        inputClassName="pr-9"
      />
      <span
        aria-hidden="true"
        className="pointer-events-none absolute inset-px flex items-center justify-end px-3"
      >
        <ChevronDown className="size-4 text-fg-quaternary stroke-[2.25px]" />
      </span>
    </div>
  );
}

/** Searchable selection reuses source InputBase and the Select option/popup components.
 * The adapter owns draft search and modal integration; consumers supply search data.
 * Keep the search input visible when positioning a menu, rather than covering it
 * with the selected-item alignment used by a non-editable Select. */
export function ComboBox({
  label,
  name,
  value,
  items,
  onChange,
  placeholder = "Search",
  className,
  size = "md",
  defaultFilter,
  labelAction,
}: {
  label: string;
  name?: string;
  value: string;
  items: ComboBoxOption[];
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  size?: ControlSize;
  defaultFilter?: (text: string, query: string) => boolean;
  labelAction?: ReactNode;
}) {
  const selected = items.find((item) => item.id === value)?.label ?? value;
  const [input, setInput] = useState({ value, text: selected });
  const setText = (text: string) => setInput({ value, text });
  const choose = (next: string) => {
    const item = items.find((item) => item.id === next);
    if (!item) return;
    if (item.onAction) {
      setInput({ value, text: "" });
      item.onAction();
      return;
    }
    setInput({ value: next, text: item.label });
    if (next !== value) onChange(next);
  };
  const surface = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLDivElement>(null);
  const dismiss = useRef<() => void>(() => {});
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<
    ReturnType<typeof choiceMenuPosition>
  >({
    placement: "bottom start",
    maxHeight: menuHeights[size],
  });
  const getContainer = useCallback(
    () =>
      surface.current?.closest<HTMLElement>(
        '[role="dialog"][data-state="open"]',
      ) ?? document.body,
    [],
  );
  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const view = trigger.ownerDocument.defaultView!;
    const viewportTop = view.visualViewport?.offsetTop ?? 0;
    const viewport = {
      top: viewportTop,
      bottom: viewportTop + (view.visualViewport?.height ?? view.innerHeight),
    };
    const dialog = surface.current?.closest<HTMLElement>(
      '[role="dialog"][data-state="open"]',
    );
    const rect = trigger.getBoundingClientRect();
    const boundary = dialog?.getBoundingClientRect() ?? viewport;
    const top = Math.max(boundary.top, viewport.top);
    const bottom = Math.min(boundary.bottom, viewport.bottom);
    // A resize can move the focused field outside the visible frame. Dismiss
    // its menu rather than leaving a detached popup beyond the dialog.
    if (bottom > top && (rect.bottom < top || rect.top > bottom)) {
      dismiss.current();
      return;
    }
    const next = choiceMenuPosition(
      rect,
      boundary,
      viewport,
      menuHeights[size],
    );
    setPosition((prior) =>
      prior.placement === next.placement && prior.maxHeight === next.maxHeight
        ? prior
        : next,
    );
  }, [size]);
  useLayoutEffect(() => {
    if (!open) return;
    updatePosition();
    const observer = new ResizeObserver(updatePosition);
    const trigger = triggerRef.current;
    const dialog = surface.current?.closest<HTMLElement>(
      '[role="dialog"][data-state="open"]',
    );
    if (trigger) observer.observe(trigger);
    if (dialog) observer.observe(dialog);
    const body = dialog?.querySelector(".dialog-body");
    body?.addEventListener("scroll", updatePosition);
    window.addEventListener("resize", updatePosition);
    window.visualViewport?.addEventListener("resize", updatePosition);
    return () => {
      observer.disconnect();
      body?.removeEventListener("scroll", updatePosition);
      window.removeEventListener("resize", updatePosition);
      window.visualViewport?.removeEventListener("resize", updatePosition);
    };
  }, [open, updatePosition]);
  return (
    <div ref={surface} className={cn("w-full min-w-0", className)}>
      <UNSAFE_PortalProvider getContainer={getContainer}>
        <SelectContext.Provider value={{ size }}>
          <AriaComboBox<ComboBoxOption>
            className="flex flex-col gap-1.5"
            menuTrigger="input"
            value={value || null}
            onOpenChange={(next, trigger) => {
              setOpen(next);
              if (next && trigger !== "input") setText("");
            }}
            inputValue={input.value === value ? input.text : selected}
            onInputChange={setText}
            onChange={(key) => {
              if (key !== null) choose(String(key));
              else setText(selected);
            }}
            defaultFilter={defaultFilter}
          >
            {labelAction ? (
              <div className="flex items-center justify-between gap-3">
                <Label isRequired={false}>{label}</Label>
                {labelAction}
              </div>
            ) : (
              <Label isRequired={false}>{label}</Label>
            )}
            <ComboBoxInput
              placeholder={placeholder}
              size={size}
              dismissRef={dismiss}
              triggerRef={triggerRef}
            />
            <Popover
              size={size}
              maxHeight={position.maxHeight}
              containerPadding={8}
              offset={4}
              placement={position.placement}
              shouldFlip={false}
              style={
                {
                  "--vult-combobox-menu-height": `${position.maxHeight}px`,
                } as CSSProperties
              }
              className="vult-select-menu vult-combobox-menu max-h-(--vult-combobox-menu-height)!"
            >
              <ListBox
                items={items}
                shouldSelectOnPressUp={false}
                className="outline-hidden"
              >
                {(item) => <Select.Item id={item.id} label={item.label} />}
              </ListBox>
            </Popover>
          </AriaComboBox>
        </SelectContext.Provider>
      </UNSAFE_PortalProvider>
      <select
        hidden
        aria-hidden="true"
        aria-label={label}
        name={name}
        tabIndex={-1}
        value={value}
        onChange={(event) => choose(event.target.value)}
      >
        <option value="" disabled />
        {items.map((item) => (
          <option key={item.id} value={item.id} disabled={!!item.onAction}>
            {item.label}
          </option>
        ))}
      </select>
    </div>
  );
}
