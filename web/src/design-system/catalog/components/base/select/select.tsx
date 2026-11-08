"use client";

import type { FC, ReactNode, Ref, RefAttributes } from "react";
import { isValidElement } from "react";
import { ChevronDown } from "../../../../icons";
import type { SelectProps as AriaSelectProps } from "react-aria-components";
import {
  Button as AriaButton,
  ListBox as AriaListBox,
  Select as AriaSelect,
  SelectValue as AriaSelectValue,
} from "react-aria-components";
import { Avatar } from "@vult/components/base/avatar/avatar";
import { HintText } from "@vult/components/base/input/hint-text";
import { Label } from "@vult/components/base/input/label";
import { Tooltip } from "@vult/components/base/tooltip/tooltip";
import { cx } from "@vult/utils/cx";
import { isReactComponent } from "@vult/utils/is-react-component";
import { ComboBox } from "./combobox";
import { Popover } from "./popover";
import { SelectItem } from "./select-item";
import {
  type CommonProps,
  SelectContext,
  type SelectItemType,
} from "./select-shared";

export {
  SelectContext,
  sizes,
  type CommonProps,
  type SelectItemType,
} from "./select-shared";

export interface SelectProps
  extends
    Omit<AriaSelectProps<SelectItemType>, "children" | "items">,
    RefAttributes<HTMLDivElement>,
    CommonProps {
  variant?: "default" | "inline" | "property" | "icon" | "inspector";
  isCollapsed?: boolean;
  inspectorSide?: "left" | "right";
  items?: SelectItemType[];
  popoverClassName?: string;
  icon?: FC | ReactNode;
  children: ReactNode | ((item: SelectItemType) => ReactNode);
}

interface SelectValueProps {
  variant?: SelectProps["variant"];
  isCollapsed?: boolean;
  inspectorSide?: SelectProps["inspectorSide"];
  isOpen: boolean;
  size: "sm" | "md" | "lg";
  isFocused: boolean;
  isDisabled: boolean;
  placeholder?: string;
  ref?: Ref<HTMLButtonElement>;
  icon?: FC | ReactNode;
  label?: string;
}

const SelectValue = ({
  isOpen,
  isFocused,
  isDisabled,
  size,
  placeholder,
  icon,
  label,
  ref,
  variant,
  isCollapsed,
  inspectorSide,
}: SelectValueProps) => {
  const property = variant === "property" || variant === "inspector";
  const compact =
    variant === "icon" || (variant === "inspector" && isCollapsed);
  const control = (
    <AriaButton
      ref={ref}
      data-control-size={size}
      data-variant={variant}
      className={cx(
        "vult-input-control vult-select-control relative flex w-full min-w-0 cursor-pointer items-center outline-hidden",
        isFocused && !isOpen && "vult-focus-visible vult-input-focused",
        isDisabled && "cursor-not-allowed opacity-50",
      )}
    >
      {property && label ? (
        <span
          aria-hidden="true"
          className="vult-select-property-label vult-type-metadata text-left text-tertiary"
        >
          {label}
        </span>
      ) : null}
      <AriaSelectValue<SelectItemType> className="vult-select-value flex h-full w-full min-w-0 items-center gap-2 px-3 text-left *:data-icon:shrink-0 *:data-icon:text-fg-quaternary">
        {(state) => {
          const selectedItem = state.selectedItems[0];
          const Icon = selectedItem?.icon || icon;
          const graphic = isReactComponent(Icon) ? (
            <Icon data-icon aria-hidden="true" />
          ) : isValidElement(Icon) ? (
            Icon
          ) : null;

          return (
            <>
              {selectedItem?.avatarUrl && variant !== "icon" ? (
                <Avatar
                  size="xs"
                  src={selectedItem.avatarUrl}
                  alt={selectedItem.label}
                  className={cx(size === "sm" && "size-5")}
                />
              ) : !property && variant !== "icon" ? (
                graphic
              ) : null}

              {selectedItem ? (
                <span
                  className={cx(
                    "flex min-w-0 flex-1 gap-1.5",
                    variant === "icon" && "sr-only",
                  )}
                >
                  <span
                    className={cx(
                      property ? "vult-type-label" : "vult-type-ui",
                      "truncate text-primary",
                    )}
                  >
                    {selectedItem?.label}
                  </span>
                  {selectedItem?.supportingText && (
                    <span className="vult-type-ui truncate text-tertiary">
                      {selectedItem?.supportingText}
                    </span>
                  )}
                </span>
              ) : (
                <span
                  className={cx(
                    "vult-type-ui min-w-0 flex-1 truncate text-placeholder",
                    variant === "icon" && "sr-only",
                  )}
                >
                  {placeholder}
                </span>
              )}

              {property || variant === "icon" ? graphic : null}
              <ChevronDown
                aria-hidden="true"
                className={cx(
                  "shrink-0 text-fg-quaternary",
                  (variant === "icon" || (property && graphic)) && "hidden",
                )}
              />
            </>
          );
        }}
      </AriaSelectValue>
    </AriaButton>
  );
  return (variant === "icon" || variant === "inspector") && label ? (
    <Tooltip
      title={label}
      placement={inspectorSide === "left" ? "right" : "left"}
      isDisabled={!compact || isDisabled || isOpen}
    >
      {control}
    </Tooltip>
  ) : (
    control
  );
};

const Select = ({
  placeholder = "Select",
  icon,
  size = "md",
  variant = "default",
  isCollapsed = false,
  inspectorSide = "right",
  children,
  items,
  label,
  hint,
  tooltip,
  hideRequiredIndicator,
  className,
  ...rest
}: SelectProps) => {
  const compact =
    variant === "icon" || (variant === "inspector" && isCollapsed);
  const property = variant === "property" || variant === "inspector";
  return (
    <SelectContext.Provider value={{ size }}>
      <AriaSelect
        {...rest}
        className={(state) =>
          cx(
            "flex min-w-0 flex-col gap-1.5",
            typeof className === "function" ? className(state) : className,
          )
        }
      >
        {(state) => (
          <>
            {label &&
              ((!property && variant !== "icon") || !rest["aria-label"]) && (
                <Label
                  className={
                    property || variant === "icon" ? "sr-only" : undefined
                  }
                  isRequired={hideRequiredIndicator ? false : state.isRequired}
                  tooltip={tooltip}
                >
                  {label}
                </Label>
              )}

            <SelectValue
              {...state}
              {...{
                size,
                placeholder,
                variant,
                label,
                isCollapsed,
                inspectorSide,
              }}
              icon={icon}
            />

            <Popover
              size={size}
              alignToSelectedItem={!compact}
              {...(compact
                ? {
                    placement:
                      inspectorSide === "left"
                        ? ("right top" as const)
                        : ("left top" as const),
                    containerPadding: 12,
                    offset: 8,
                  }
                : {})}
              className={cx(
                "vult-select-menu",
                rest.popoverClassName,
              )}
            >
              {/* The menu covers its trigger: releasing the opening click must not select the row underneath. */}
              <AriaListBox
                items={items}
                shouldSelectOnPressUp={false}
                className="vult-menu-list size-full outline-hidden"
              >
                {children}
              </AriaListBox>
            </Popover>

            {hint && (
              <HintText
                isInvalid={state.isInvalid}
                className={cx(size === "sm" && "text-xs")}
              >
                {hint}
              </HintText>
            )}
          </>
        )}
      </AriaSelect>
    </SelectContext.Provider>
  );
};

const _Select = Select as typeof Select & {
  ComboBox: typeof ComboBox;
  Item: typeof SelectItem;
};
_Select.ComboBox = ComboBox;
_Select.Item = SelectItem;

export { _Select as Select };
