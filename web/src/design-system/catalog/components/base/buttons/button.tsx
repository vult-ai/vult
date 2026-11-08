"use client";

import type {
  AnchorHTMLAttributes,
  ButtonHTMLAttributes,
  DetailedHTMLProps,
  FC,
  MouseEvent as ReactMouseEvent,
  ReactNode,
} from "react";
import React, { isValidElement } from "react";
import type {
  ButtonProps as AriaButtonProps,
  LinkProps as AriaLinkProps,
} from "react-aria-components";
import { Button as AriaButton, Link as AriaLink } from "react-aria-components";
import { cx, sortCx } from "@vult/utils/cx";
import { isReactComponent } from "@vult/utils/is-react-component";
import { useButtonAction } from "./use-button-action";

export const styles = sortCx({
  common: {
    root: "group vult-button in-data-input-wrapper:in-data-leading:rounded-r-none in-data-input-wrapper:in-data-trailing:rounded-l-none",
    icon: "pointer-events-none size-4 shrink-0",
  },
  // Button standard v1 (docs/design/buttons.md): three sizes, five colours.
  sizes: {
    sm: { root: "vult-button-sm", linkRoot: "" },
    md: { root: "vult-button-md", linkRoot: "" },
    lg: { root: "vult-button-lg", linkRoot: "" },
  },
  colors: {
    primary: { root: "vult-button-primary" },
    secondary: { root: "vult-button-secondary" },
    tertiary: { root: "vult-button-ghost" },
    link: { root: "vult-button-link" },
    /** Only inside the shared confirmation; see docs/design/feedback.md. */
    destructive: { root: "vult-button-destructive" },
  },
});

/**
 * Common props shared between button and anchor variants
 */
export interface CommonProps {
  /** Disables the button and shows a disabled state */
  isDisabled?: boolean;
  /** Shows a loading spinner and disables the button */
  isLoading?: boolean;
  /** The size variant of the button */
  size?: keyof typeof styles.sizes;
  /** The color variant of the button */
  color?: keyof typeof styles.colors;
  /** Icon component or element to show before the text */
  iconLeading?: FC<{ className?: string }> | ReactNode;
  /** Icon component or element to show after the text */
  iconTrailing?: FC<{ className?: string }> | ReactNode;
  /** Removes horizontal padding from the text content */
  noTextPadding?: boolean;
  /** When true, keeps the text visible during loading state */
  showTextWhileLoading?: boolean;
  /** Keeps a pending button's label stable without adding a spinner. */
  hideLoadingIndicator?: boolean;
}

/**
 * Props for the button variant (non-link)
 */
export interface ButtonProps
  extends
    CommonProps,
    DetailedHTMLProps<
      Omit<ButtonHTMLAttributes<HTMLButtonElement>, "color" | "slot">,
      HTMLButtonElement
    > {
  /** Slot name for react-aria component */
  slot?: AriaButtonProps["slot"];
  /** React Aria press handler */
  onPress?: AriaButtonProps["onPress"];
}

/**
 * Props for the link variant (anchor tag)
 */
interface LinkProps
  extends
    CommonProps,
    DetailedHTMLProps<
      Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "color">,
      HTMLAnchorElement
    > {
  /** Options for the configured client side router. */
  routerOptions?: AriaLinkProps["routerOptions"];
  /** React Aria press handler */
  onPress?: AriaLinkProps["onPress"];
}

/** Union type of button and link props */
export type Props = ButtonProps | LinkProps;

export const Button = ({
  size = "md",
  color = "primary",
  children,
  className,
  noTextPadding,
  iconLeading: IconLeading,
  iconTrailing: IconTrailing,
  isDisabled: disabled,
  isLoading: loading,
  showTextWhileLoading,
  hideLoadingIndicator,
  ...otherProps
}: Props) => {
  const href = "href" in otherProps ? otherProps.href : undefined;
  const Component = href ? AriaLink : AriaButton;
  const buttonProps = otherProps as ButtonProps;
  const action = useButtonAction(
    href
      ? undefined
      : (buttonProps.onPress ??
          (buttonProps.onClick
            ? (event) =>
                buttonProps.onClick!(
                  event as unknown as ReactMouseEvent<HTMLButtonElement>,
                )
            : undefined)),
    disabled || loading,
    href ? "button" : buttonProps.type,
  );
  if (action.pending && !loading) showTextWhileLoading ??= true;
  loading = loading || action.pending;
  disabled = disabled || action.pending;

  const isIcon = (IconLeading || IconTrailing) && !children;
  const isLinkType = color === "link";

  noTextPadding = isLinkType || noTextPadding;

  let props = {};

  if (href) {
    props = {
      ...otherProps,

      href: disabled ? undefined : href,
    };
  } else {
    const { onClick, onPress, ...restButtonProps } = buttonProps;

    props = {
      ...restButtonProps,

      type: buttonProps.type || "button",
      isPending: loading,
      onPress: action.onPress,
      onClickCapture: (event: ReactMouseEvent<HTMLButtonElement>) => {
        buttonProps.onClickCapture?.(event);
        if (!event.defaultPrevented) action.onClickCapture?.(event);
      },
    };
  }

  return (
    <Component
      data-loading={loading ? true : undefined}
      data-icon-only={isIcon ? true : undefined}
      {...props}
      isDisabled={disabled}
      className={cx(
        styles.common.root,
        styles.sizes[size].root,
        styles.colors[color].root,
        isLinkType && styles.sizes[size].linkRoot,
        (loading || (href && (disabled || loading))) && "pointer-events-none",
        // If in `loading` state, hide everything except the loading icon (and text if `showTextWhileLoading` is true).
        loading &&
          (showTextWhileLoading
            ? "[&>*:not([data-icon=loading]):not([data-text])]:hidden"
            : "[&>*:not([data-icon=loading])]:invisible"),
        className,
      )}
    >
      {/* Leading icon */}
      {isValidElement(IconLeading) && IconLeading}
      {isReactComponent(IconLeading) && (
        <IconLeading data-icon="leading" className={styles.common.icon} />
      )}

      {loading && !hideLoadingIndicator && (
        <svg
          fill="none"
          data-icon="loading"
          viewBox="0 0 20 20"
          className={cx(
            styles.common.icon,
            !showTextWhileLoading &&
              "absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2",
          )}
        >
          {/* Background circle */}
          <circle
            className="stroke-current opacity-30"
            cx="10"
            cy="10"
            r="8"
            fill="none"
            strokeWidth="2"
          />
          {/* Spinning circle */}
          <circle
            className="origin-center animate-spin stroke-current"
            cx="10"
            cy="10"
            r="8"
            fill="none"
            strokeWidth="2"
            strokeDasharray="12.5 50"
            strokeLinecap="round"
          />
        </svg>
      )}

      {children && (
        <span
          data-text
          className={cx("transition-inherit-all", !noTextPadding && "px-0.5")}
        >
          {children}
        </span>
      )}

      {/* Trailing icon */}
      {isValidElement(IconTrailing) && IconTrailing}
      {isReactComponent(IconTrailing) && (
        <IconTrailing data-icon="trailing" className={styles.common.icon} />
      )}
    </Component>
  );
};
