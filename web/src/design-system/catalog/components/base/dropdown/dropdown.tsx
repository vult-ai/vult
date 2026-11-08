"use client";

import {
  type FC,
  type ReactNode,
  type RefAttributes,
  useCallback,
} from "react";
import { Check, ChevronRight, DotsVertical } from "../../../../icons";
import type {
  ButtonProps as AriaButtonProps,
  MenuItemProps as AriaMenuItemProps,
  MenuProps as AriaMenuProps,
  PopoverProps as AriaPopoverProps,
  SeparatorProps as AriaSeparatorProps,
  MenuItemRenderProps,
} from "react-aria-components";
import {
  Button as AriaButton,
  Header as AriaHeader,
  Menu as AriaMenu,
  MenuItem as AriaMenuItem,
  MenuSection as AriaMenuSection,
  MenuTrigger as AriaMenuTrigger,
  Popover as AriaPopover,
  Separator as AriaSeparator,
} from "react-aria-components";
import { cx } from "@vult/utils/cx";
import { Avatar } from "../avatar/avatar";
import { CheckboxBase } from "../checkbox/checkbox";
import { RadioButtonBase } from "../radio-buttons/radio-buttons";
import { ToggleBase } from "../toggle/toggle";

interface DropdownItemProps extends AriaMenuItemProps {
  /** The label of the item to be displayed. */
  label?: string;
  /** An addon to be displayed on the right side of the item. */
  addon?: ReactNode;
  /** If true, the item will not have any styles. */
  unstyled?: boolean;
  /** An icon to be displayed on the left side of the item. */
  icon?: FC<{ className?: string }>;
  /** Avatar URL to be displayed on the left side of the item. */
  avatarUrl?: string;
  /** The selection indicator to be displayed on the item. */
  selectionIndicator?: "checkmark" | "checkbox" | "radio" | "toggle" | "none";
  /** Use the same option styling as Vult's input controls. */
  appearance?: "default" | "control";
  /** Destructive actions use the error color for both their label and icon. */
  isDestructive?: boolean;
}

const DropdownItem = ({
  label,
  children,
  addon,
  icon: Icon,
  avatarUrl,
  unstyled,
  selectionIndicator = "checkmark",
  appearance = "default",
  isDestructive = false,
  ...props
}: DropdownItemProps) => {
  const SelectionIndicator = useCallback(
    (state: MenuItemRenderProps & { className?: string }) => {
      if (selectionIndicator === "checkmark") {
        return (
          <Check
            aria-hidden="true"
            className={cx(
              "size-4 shrink-0 stroke-[2.25px] text-fg-brand-primary",
              !state.isSelected && "invisible",
              state.className,
            )}
          />
        );
      }
      if (selectionIndicator === "checkbox") {
        return (
          <CheckboxBase
            isSelected={state.isSelected && !state.hasSubmenu}
            isIndeterminate={state.isSelected && state.hasSubmenu}
            size="sm"
            className={cx("shrink-0", state.className)}
          />
        );
      }
      if (selectionIndicator === "radio") {
        return (
          <RadioButtonBase
            isSelected={state.isSelected}
            className={cx("shrink-0", state.className)}
          />
        );
      }
      if (selectionIndicator === "toggle") {
        return (
          <ToggleBase
            slim
            size="sm"
            isSelected={state.isSelected}
            className={cx("shrink-0", state.className)}
          />
        );
      }
      return null;
    },
    [selectionIndicator],
  );

  if (unstyled) {
    return <AriaMenuItem id={label} textValue={label} {...props} />;
  }

  return (
    <AriaMenuItem
      {...props}
      className={(state) =>
        cx(
          "group block cursor-pointer outline-hidden",
          state.isDisabled && "cursor-not-allowed opacity-50",
          typeof props.className === "function"
            ? props.className(state)
            : props.className,
        )
      }
    >
      {(state) => (
        <div
          data-selected={state.isSelected ? true : undefined}
          data-menu-option={appearance === "control" || undefined}
          data-highlighted={
            (appearance === "control" && state.isFocused) || undefined
          }
          className={cx(
            "vult-menu-row vult-action-target vult-action-fill relative outline-focus-ring",
            state.isFocused && "bg-primary_hover",
            state.isFocusVisible && "vult-focus-visible vult-focus-contained",
          )}
        >
          {state.selectionMode !== "none" && !avatarUrl && !Icon && (
            <SelectionIndicator {...state} />
          )}

          {avatarUrl && (
            <div data-menu-icon className="flex items-center justify-center">
              <Avatar
                aria-hidden="true"
                size="xs"
                src={avatarUrl}
                alt={label}
                className="size-4"
              />
            </div>
          )}

          {Icon && (
            <Icon
              aria-hidden="true"
              className={cx(
                "stroke-[2.25px]",
                isDestructive ? "text-error-primary" : "text-fg-quaternary",
              )}
            />
          )}

          <span
            data-menu-label={appearance === "control" || undefined}
            // Labels past the maximum width truncate; the full text shows on hover.
            title={label ?? (typeof children === "string" ? children : undefined)}
            className={cx(
              "vult-type-ui min-w-0 grow truncate",
              isDestructive ? "text-error-primary" : "text-secondary",
              state.isFocused && !isDestructive && "text-secondary_hover",
            )}
          >
            {label ||
              (typeof children === "function" ? children(state) : children)}
          </span>

          {addon && (
            <span
              data-menu-addon
              className="vult-type-metadata shrink-0 text-quaternary"
            >
              {addon}
            </span>
          )}

          {state.selectionMode !== "none" && (avatarUrl || Icon) && (
            <SelectionIndicator {...state} />
          )}

          {state.hasSubmenu && (
            <ChevronRight
              aria-hidden="true"
              className="stroke-[2.25px] text-fg-quaternary"
            />
          )}
        </div>
      )}
    </AriaMenuItem>
  );
};

interface DropdownMenuProps<T extends object> extends AriaMenuProps<T> {}

const DropdownMenu = <T extends object>(props: DropdownMenuProps<T>) => {
  return (
    <AriaMenu
      {...props}
      className={(state) =>
        cx(
          "vult-menu-list h-min overflow-y-auto outline-hidden select-none",
          typeof props.className === "function"
            ? props.className(state)
            : props.className,
        )
      }
    />
  );
};

interface DropdownPopoverProps extends AriaPopoverProps {}

const DropdownPopover = (props: DropdownPopoverProps) => {
  return (
    <AriaPopover
      data-vult-surface="overlay"
      placement="bottom right"
      {...props}
      className={(state) =>
        cx(
          "vult-menu origin-(--trigger-anchor-point) overflow-auto outline-hidden rounded-lg bg-primary shadow-lg ring-1 ring-secondary_alt will-change-transform",
          state.isEntering &&
            "duration-[var(--vult-motion-overlay-enter)] ease-[var(--vult-ease-settle)] animate-in fade-in placement-right:slide-in-from-left-0.5 placement-top:slide-in-from-bottom-0.5 placement-bottom:slide-in-from-top-0.5",
          state.isExiting &&
            "duration-[var(--vult-motion-overlay-exit)] ease-[var(--vult-ease-settle)] animate-out fade-out placement-right:slide-out-to-left-0.5 placement-top:slide-out-to-bottom-0.5 placement-bottom:slide-out-to-top-0.5",
          typeof props.className === "function"
            ? props.className(state)
            : props.className,
        )
      }
    >
      {props.children}
    </AriaPopover>
  );
};

const DropdownSeparator = (props: AriaSeparatorProps) => {
  return (
    <AriaSeparator
      {...props}
      className={cx("vult-menu-separator h-px w-full bg-border-secondary", props.className)}
    />
  );
};

const DropdownDotsButton = (
  props: AriaButtonProps & RefAttributes<HTMLButtonElement>,
) => {
  return (
    <AriaButton
      {...props}
      aria-label={props["aria-label"] ?? "Open menu"}
      className={(state) =>
        cx(
          "vult-action-target vult-action-fill flex size-8 cursor-pointer items-center justify-center rounded-md text-fg-quaternary outline-focus-ring",
          (state.isPressed || state.isHovered) && "text-fg-quaternary_hover",
          typeof props.className === "function"
            ? props.className(state)
            : props.className,
        )
      }
    >
      <DotsVertical className="size-5 transition-inherit-all" />
    </AriaButton>
  );
};

export const Dropdown = {
  Root: AriaMenuTrigger,
  Popover: DropdownPopover,
  Menu: DropdownMenu,
  Section: AriaMenuSection,
  SectionHeader: AriaHeader,
  Item: DropdownItem,
  Separator: DropdownSeparator,
  DotsButton: DropdownDotsButton,
};
