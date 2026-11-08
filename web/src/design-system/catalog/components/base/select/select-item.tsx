"use client";

import { isValidElement, useContext } from "react";
import { Check } from "../../../../icons";
import type { ListBoxItemProps as AriaListBoxItemProps } from "react-aria-components";
import {
  ListBoxItem as AriaListBoxItem,
  Text as AriaText,
} from "react-aria-components";
import { Avatar } from "@vult/components/base/avatar/avatar";
import { CheckboxBase } from "@vult/components/base/checkbox/checkbox";
import { cx } from "@vult/utils/cx";
import { isReactComponent } from "@vult/utils/is-react-component";
import type { SelectItemType } from "./select-shared";
import { SelectContext } from "./select-shared";

// Every option row follows the menu standard (docs/design/menus.md): 32px
// rows, 16px icons and checks, interface-size text, whatever the trigger size.
const row = {
  root: "*:data-icon:stroke-[2.25px]",
  text: "vult-type-ui",
  textContainer: "gap-x-1.5",
  check: "stroke-[2.25px]",
};
const sizes = {
  sm: { ...row, checkbox: "sm" as const },
  md: { ...row, checkbox: "sm" as const },
  lg: { ...row, checkbox: "md" as const },
};

interface SelectItemProps
  extends Omit<AriaListBoxItemProps<SelectItemType>, "id">, SelectItemType {
  /** Leading menu icon that does not replace the select's trigger icon. */
  menuIcon?: SelectItemType["icon"];
  /** The selection indicator to be displayed on the item. */
  selectionIndicator?: "checkmark" | "checkbox" | "none";
  /** The alignment of the selection indicator. */
  selectionIndicatorAlign?: "left" | "right";
}

export const SelectItem = ({
  label,
  id,
  value,
  avatarUrl,
  supportingText,
  textValue,
  isDisabled,
  icon,
  menuIcon,
  className,
  children,
  selectionIndicator = "checkmark",
  selectionIndicatorAlign = "right",
  ...props
}: SelectItemProps) => {
  const { size } = useContext(SelectContext);
  const Icon = menuIcon ?? icon;

  const labelOrChildren =
    label || (typeof children === "string" ? children : "");
  const itemTextValue =
    textValue ??
    (supportingText ? labelOrChildren + " " + supportingText : labelOrChildren);

  const isLeft = selectionIndicatorAlign === "left";

  return (
    <AriaListBoxItem
      id={id}
      value={
        value ?? {
          id,
          label: labelOrChildren,
          avatarUrl,
          supportingText,
          textValue: itemTextValue,
          isDisabled,
          icon,
        }
      }
      textValue={itemTextValue}
      isDisabled={isDisabled}
      {...props}
      className={(state) =>
        cx(
          "w-full outline-hidden",
          typeof className === "function" ? className(state) : className,
        )
      }
    >
      {(state) => (
        <div
          data-select-option
          data-highlighted={state.isFocused || state.isHovered || undefined}
          className={cx(
            "vult-menu-row cursor-pointer outline-hidden select-none",
            (state.isFocused ||
              state.isHovered ||
              (state.isSelected && selectionIndicator !== "checkbox")) &&
              "bg-primary_hover",
            state.isDisabled && "cursor-not-allowed opacity-50",
            state.isFocusVisible && "vult-focus-visible vult-focus-contained",

            // Icon styles
            "*:data-icon:shrink-0 *:data-icon:text-fg-quaternary",

            sizes[size].root,
          )}
        >
          {isLeft && selectionIndicator === "checkbox" && (
            <CheckboxBase
              size={sizes[size].checkbox}
              isSelected={state.isSelected}
              isDisabled={state.isDisabled}
            />
          )}

          {avatarUrl ? (
            <Avatar
              aria-hidden="true"
              size="xs"
              src={avatarUrl}
              alt={label}
              data-menu-icon
              className="size-4"
            />
          ) : isReactComponent(Icon) ? (
            <Icon data-icon aria-hidden="true" />
          ) : isValidElement(Icon) ? (
            Icon
          ) : null}

          <div
            className={cx(
              "flex w-full min-w-0 flex-1 flex-wrap",
              sizes[size].textContainer,
            )}
          >
            <AriaText
              slot="label"
              title={label ?? (typeof children === "string" ? children : undefined)}
              className={cx(
                "truncate whitespace-nowrap text-primary",
                sizes[size].text,
              )}
            >
              {label ||
                (typeof children === "function" ? children(state) : children)}
            </AriaText>

            {supportingText && (
              <AriaText
                slot="description"
                className={cx(
                  "whitespace-nowrap text-tertiary",
                  sizes[size].text,
                )}
              >
                {supportingText}
              </AriaText>
            )}
          </div>

          {state.isSelected && selectionIndicator === "checkmark" && (
            <Check
              data-select-check
              aria-hidden="true"
              className={cx(
                "ml-auto text-fg-brand-primary",
                sizes[size].check,
              )}
            />
          )}

          {!isLeft && selectionIndicator === "checkbox" && (
            <CheckboxBase
              size={sizes[size].checkbox}
              isSelected={state.isSelected}
              isDisabled={state.isDisabled}
              className="ml-auto"
            />
          )}
        </div>
      )}
    </AriaListBoxItem>
  );
};
