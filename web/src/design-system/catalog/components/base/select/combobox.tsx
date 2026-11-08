"use client";

import type {
  FC,
  FocusEventHandler,
  PointerEventHandler,
  ReactNode,
  RefAttributes,
  RefObject,
} from "react";
import { isValidElement, useContext, useEffect, useRef } from "react";
import { ChevronDown, SearchLg } from "../../../../icons";
import type {
  ComboBoxProps as AriaComboBoxProps,
  GroupProps as AriaGroupProps,
  ListBoxProps as AriaListBoxProps,
} from "react-aria-components";
import {
  ComboBox as AriaComboBox,
  Group as AriaGroup,
  Input as AriaInput,
  ListBox as AriaListBox,
  ComboBoxStateContext,
} from "react-aria-components";
import { HintText } from "@vult/components/base/input/hint-text";
import { Label } from "@vult/components/base/input/label";
import { Popover } from "@vult/components/base/select/popover";
import {
  type CommonProps,
  SelectContext,
  type SelectItemType,
  sizes,
} from "@vult/components/base/select/select-shared";
import { cx } from "@vult/utils/cx";
import { isReactComponent } from "@vult/utils/is-react-component";

interface ComboBoxProps
  extends
    Omit<AriaComboBoxProps<SelectItemType>, "children" | "items">,
    RefAttributes<HTMLDivElement>,
    CommonProps {
  shortcut?: boolean;
  showChevron?: boolean;
  showSearchIcon?: boolean;
  dismissToken?: number;
  items?: SelectItemType[];
  popoverClassName?: string;
  fitPopoverToContent?: boolean;
  plainInput?: boolean;
  shortcutClassName?: string;
  /** Leading icon component displayed before the input. */
  icon?: FC | ReactNode;
  children: AriaListBoxProps<SelectItemType>["children"];
}

interface ComboBoxValueProps extends AriaGroupProps {
  size: "sm" | "md" | "lg";
  shortcut: boolean;
  showChevron: boolean;
  showSearchIcon: boolean;
  dismissToken?: number;
  placeholder?: string;
  plainInput?: boolean;
  shortcutClassName?: string;
  icon?: FC | ReactNode;
  onFocus?: FocusEventHandler;
  onPointerEnter?: PointerEventHandler;
  ref?: RefObject<HTMLDivElement | null>;
}

const ComboBoxValue = ({
  size,
  shortcut,
  showChevron,
  showSearchIcon,
  dismissToken,
  placeholder,
  plainInput,
  shortcutClassName,
  icon: IconProp,
  ...otherProps
}: ComboBoxValueProps) => {
  const state = useContext(ComboBoxStateContext);
  const stateRef = useRef(state);

  stateRef.current = state;

  const value = state?.selectedItem?.value || null;
  const inputValue = state?.inputValue || null;

  const first = inputValue?.split(value?.supportingText)?.[0] || "";
  const last = inputValue?.split(first)[1];

  useEffect(() => {
    if (!dismissToken) return;

    window.requestAnimationFrame(() => {
      stateRef.current?.setFocused(false);
      stateRef.current?.close();
    });
  }, [dismissToken]);

  return (
    <AriaGroup
      {...otherProps}
      onClick={(event) => {
        const input = event.currentTarget.querySelector<HTMLInputElement>("input");
        if (!input || input.disabled || input.readOnly) return;
        input.focus();
        if (!stateRef.current?.isOpen) stateRef.current?.open(null, "manual");
      }}
      className={({ isFocusWithin, isDisabled }) =>
        cx(
          "relative flex min-h-11 w-full items-center gap-2 rounded-lg bg-primary shadow-xs ring-1 ring-primary outline-hidden transition-shadow duration-100 ease-linear ring-inset",
          isDisabled && "cursor-not-allowed opacity-50",
          (isFocusWithin || state?.isOpen) && "vult-focus-visible",

          // Icon styles
          "*:data-icon:shrink-0 *:data-icon:text-fg-quaternary",

          sizes[size].root,
        )
      }
    >
      {showSearchIcon ? (
        <SearchLg
          data-icon
          className="pointer-events-none"
          aria-hidden="true"
        />
      ) : isReactComponent(IconProp) ? (
        <IconProp
          data-icon
          className="pointer-events-none"
          aria-hidden="true"
        />
      ) : isValidElement(IconProp) ? (
        IconProp
      ) : null}

      <div className="relative flex w-full items-center">
        {!plainInput && inputValue && (
          <span
            className={cx(
              "absolute top-1/2 z-0 inline-flex w-full -translate-y-1/2 truncate",
              sizes[size].textContainer,
            )}
            aria-hidden="true"
          >
            <p className={cx("font-medium text-primary", sizes[size].text)}>
              {first}
            </p>
            {last && (
              <p className={cx("-ml-0.75 text-tertiary", sizes[size].text)}>
                {last}
              </p>
            )}
          </span>
        )}

        <AriaInput
          placeholder={placeholder}
          className={cx(
            "z-10 min-h-0! w-full appearance-none rounded-none! border-0! bg-transparent! p-0! caret-alpha-black/90 shadow-none! ring-0! outline-none! placeholder:text-placeholder focus:border-0! focus:ring-0! focus:outline-none! disabled:cursor-not-allowed",
            plainInput ? "text-primary" : "text-transparent",
            sizes[size].text,
          )}
        />
      </div>

      {showChevron && (
        <ChevronDown
          aria-hidden="true"
          className={cx(
            "shrink-0 text-fg-quaternary",
            size === "lg" ? "size-5" : "size-4 stroke-[2.25px]",
          )}
        />
      )}

      {shortcut && (
        <div
          className={cx(
            "absolute inset-y-0.5 right-0.5 z-10 hidden items-center rounded-r-[inherit] bg-linear-to-r from-transparent to-bg-primary to-40% pl-8 md:flex",
            sizes[size].shortcut,
            shortcutClassName,
          )}
        >
          <span
            className="pointer-events-none rounded px-1 py-px text-xs font-medium text-quaternary ring-1 ring-secondary select-none ring-inset"
            aria-hidden="true"
          >
            ⌘K
          </span>
        </div>
      )}
    </AriaGroup>
  );
};

export const ComboBox = ({
  placeholder = "Search",
  shortcut = true,
  showChevron = false,
  showSearchIcon = true,
  dismissToken,
  size = "md",
  children,
  items,
  shortcutClassName,
  fitPopoverToContent = false,
  plainInput = false,
  icon,
  hideRequiredIndicator,
  ...otherProps
}: ComboBoxProps) => {
  const placeholderRef = useRef<HTMLDivElement>(null);
  // The list's width follows the menu standard: at least the trigger's width
  // (provided by the overlay as --trigger-width), growing with its content.

  return (
    <SelectContext.Provider value={{ size }}>
      <AriaComboBox menuTrigger="focus" {...otherProps}>
        {(state) => (
          <div className="flex flex-col gap-1.5">
            {otherProps.label && (
              <Label
                isRequired={hideRequiredIndicator ? false : state.isRequired}
                tooltip={otherProps.tooltip}
              >
                {otherProps.label}
              </Label>
            )}

            <ComboBoxValue
              ref={placeholderRef}
              placeholder={placeholder}
              plainInput={plainInput}
              shortcut={shortcut}
              showChevron={showChevron}
              showSearchIcon={showSearchIcon}
              dismissToken={dismissToken}
              shortcutClassName={shortcutClassName}
              icon={icon}
              size={size}
            />

            <Popover
              size={size}
              triggerRef={placeholderRef}
              className={otherProps.popoverClassName}
              {...(fitPopoverToContent
                ? { placement: "bottom start" as const }
                : {})}
            >
              <AriaListBox
                items={items}
                shouldSelectOnPressUp={false}
                className="vult-menu-list size-full outline-hidden"
              >
                {children}
              </AriaListBox>
            </Popover>

            {otherProps.hint && (
              <HintText
                isInvalid={state.isInvalid}
                className={cx(size === "sm" && "text-xs")}
              >
                {otherProps.hint}
              </HintText>
            )}
          </div>
        )}
      </AriaComboBox>
    </SelectContext.Provider>
  );
};
