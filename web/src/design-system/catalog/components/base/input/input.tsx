"use client";

import {
  type ComponentType,
  type HTMLAttributes,
  type ReactNode,
  type Ref,
  createContext,
  useContext,
  useState,
} from "react";
import { Eye, EyeOff, HelpCircle, InfoCircle } from "../../../../icons";
import type {
  InputProps as AriaInputProps,
  TextFieldProps as AriaTextFieldProps,
} from "react-aria-components";
import {
  Button as AriaButton,
  Group as AriaGroup,
  Input as AriaInput,
  TextField as AriaTextField,
} from "react-aria-components";
import { HintText } from "@vult/components/base/input/hint-text";
import { Label } from "@vult/components/base/input/label";
import { Tooltip, TooltipTrigger } from "@vult/components/base/tooltip/tooltip";
import { cx, sortCx } from "@vult/utils/cx";

export interface InputBaseProps extends Omit<AriaInputProps, "size"> {
  /** Tooltip message on hover. */
  tooltip?: string;
  /** Tooltip message shown from the invalid-state icon. */
  invalidTooltip?: string;
  /** Whether the input is invalid. */
  isInvalid?: boolean;
  /** Whether the input is disabled. */
  isDisabled?: boolean;
  /** Whether the input is required. */
  isRequired?: boolean;
  /**
   * Input size.
   * @default "sm"
   */
  size?: "sm" | "md" | "lg";
  /** Placeholder text. */
  placeholder?: string;
  /** Class name for the icon. */
  iconClassName?: string;
  /** Class name for the input. */
  inputClassName?: string;
  /** Class name for the input wrapper. */
  wrapperClassName?: string;
  /** Class name for the tooltip. */
  tooltipClassName?: string;
  /** Keyboard shortcut to display. */
  shortcut?: string | boolean;
  ref?: Ref<HTMLInputElement>;
  groupRef?: Ref<HTMLDivElement>;
  /** Icon component to display on the left side of the input. */
  icon?: ComponentType<HTMLAttributes<HTMLOrSVGElement>>;
}

export const InputBase = ({
  ref,
  tooltip,
  invalidTooltip,
  shortcut,
  groupRef,
  size = "md",
  isInvalid,
  isDisabled,
  isRequired,
  icon: Icon,
  placeholder,
  wrapperClassName,
  tooltipClassName,
  inputClassName,
  iconClassName,
  type = "text",
  ...inputProps
}: InputBaseProps) => {
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const [isInvalidTooltipOpen, setIsInvalidTooltipOpen] = useState(false);

  // Check if the input has a leading icon or trailing affordance.
  const hasPasswordToggle = type === "password";
  const hasTrailingIcon = tooltip || isInvalid || hasPasswordToggle;
  const hasLeadingIcon = Icon;

  // If the input is inside a `TextFieldContext`, use its context to simplify applying styles
  const context = useContext(TextFieldContext);

  const inputSize = context?.size || size;

  const sizes = sortCx({
    sm: {
      root: cx(
        "px-3",
        hasLeadingIcon && "pl-9",
        hasTrailingIcon && (hasPasswordToggle ? "pr-10" : "pr-9"),
      ),
      iconLeading: "left-3 size-4 stroke-[2.25px]",
      iconTrailing: "right-3",
      passwordToggle: "right-1.5 size-7",
      shortcut: "pr-1.5",
    },
    md: {
      root: cx(
        "px-3",
        hasLeadingIcon && "pl-10",
        hasTrailingIcon && (hasPasswordToggle ? "pr-10" : "pr-9"),
      ),
      iconLeading: "left-3 size-5",
      iconTrailing: "right-3",
      passwordToggle: "right-1.5 size-8",
      shortcut: "pr-2",
    },
    lg: {
      root: cx(
        "px-3.5",
        hasLeadingIcon && "pl-10.5",
        hasTrailingIcon && (hasPasswordToggle ? "pr-11" : "pr-9.5"),
      ),
      iconLeading: "left-3.5 size-5",
      iconTrailing: "right-3.5",
      passwordToggle: "right-2 size-8",
      shortcut: "pr-2.5",
    },
  });

  return (
    <AriaGroup
      {...{ isDisabled, isInvalid }}
      data-control-size={inputSize}
      ref={groupRef}
      className={({ isFocusWithin, isDisabled, isInvalid }) =>
        cx(
          "vult-input-control group/input relative flex w-full flex-row place-content-center place-items-center",
          isFocusWithin && !isDisabled && "vult-input-focused",
          isDisabled && "vult-input-disabled",
          isInvalid && "vult-input-invalid",

          context?.wrapperClassName,
          wrapperClassName,
        )
      }
    >
      {/* Leading icon and Payment icon */}
      {Icon && (
        <Icon
          className={cx(
            "pointer-events-none absolute text-fg-quaternary",
            sizes[inputSize].iconLeading,
            context?.iconClassName,
            iconClassName,
          )}
        />
      )}

      {/* Input field */}
      <AriaInput
        {...(inputProps as AriaInputProps)}
        ref={ref}
        required={isRequired}
        type={type === "password" && isPasswordVisible ? "text" : type}
        placeholder={placeholder}
        className={cx(
          "m-0 w-full bg-transparent text-primary ring-0 outline-hidden placeholder:text-placeholder autofill:rounded-lg autofill:text-primary disabled:cursor-not-allowed",
          sizes[inputSize].root,
          context?.inputClassName,
          inputClassName,
        )}
      />

      {/* Tooltip and help icon */}
      {tooltip && type !== "password" && (
        <Tooltip title={tooltip} placement="top">
          <TooltipTrigger
            className={cx(
              "absolute cursor-pointer text-fg-quaternary transition duration-100 ease-linear group-invalid/input:hidden hover:text-fg-quaternary_hover focus:text-fg-quaternary_hover",
              sizes[inputSize].iconTrailing,
              context?.tooltipClassName,
              tooltipClassName,
            )}
          >
            <HelpCircle className="size-4 stroke-[2.25px]" />
          </TooltipTrigger>
        </Tooltip>
      )}

      {/* Invalid icon */}
      {type !== "password" && invalidTooltip && (
        <Tooltip
          title={invalidTooltip}
          placement="top"
          delay={0}
          isOpen={isInvalidTooltipOpen}
          onOpenChange={setIsInvalidTooltipOpen}
        >
          <TooltipTrigger
            aria-label="Show validation help"
            isDisabled={false}
            onBlur={() => setIsInvalidTooltipOpen(false)}
            onFocus={() => setIsInvalidTooltipOpen(true)}
            onMouseEnter={() => setIsInvalidTooltipOpen(true)}
            onMouseLeave={() => setIsInvalidTooltipOpen(false)}
            className={cx(
              "absolute hidden cursor-pointer text-fg-error-secondary transition duration-100 ease-linear group-invalid/input:block hover:text-error-primary focus:text-error-primary",
              sizes[inputSize].iconTrailing,
              context?.tooltipClassName,
              tooltipClassName,
            )}
          >
            <InfoCircle className="size-4 stroke-[2.25px]" />
          </TooltipTrigger>
        </Tooltip>
      )}

      {type !== "password" && !invalidTooltip && (
        <InfoCircle
          className={cx(
            "pointer-events-none absolute hidden size-4 stroke-[2.25px] text-fg-error-secondary group-invalid/input:block",
            sizes[inputSize].iconTrailing,
            context?.tooltipClassName,
            tooltipClassName,
          )}
        />
      )}

      {/* Password visibility toggle */}
      {type === "password" && (
        <AriaButton
          type="button"
          aria-label="Toggle password visibility"
          onClick={() => setIsPasswordVisible(!isPasswordVisible)}
          className={cx(
            "absolute top-1/2 flex -translate-y-1/2 cursor-pointer items-center justify-center rounded-md text-fg-tertiary transition-colors duration-100 ease-linear hover:text-fg-secondary focus:outline-hidden vult-focus-target",
            sizes[inputSize].passwordToggle,
          )}
        >
          {isPasswordVisible ? (
            <EyeOff className="size-5 stroke-[2px]" />
          ) : (
            <Eye className="size-5 stroke-[2px]" />
          )}
        </AriaButton>
      )}

      {/* Shortcut */}
      {shortcut && (
        <div
          className={cx(
            "pointer-events-none absolute inset-y-0.5 right-0.5 z-10 hidden items-center rounded-r-[inherit] bg-linear-to-r from-transparent to-bg-primary to-40% pl-8 md:flex",
            sizes[inputSize].shortcut,
          )}
        >
          <span
            aria-hidden="true"
            className="pointer-events-none rounded px-1 py-px text-xs font-medium text-quaternary ring-1 ring-secondary select-none ring-inset"
          >
            {typeof shortcut === "string" ? shortcut : "⌘K"}
          </span>
        </div>
      )}
    </AriaGroup>
  );
};

InputBase.displayName = "InputBase";

interface TextFieldContextProps extends Partial<
  Pick<
    InputBaseProps,
    | "size"
    | "wrapperClassName"
    | "inputClassName"
    | "iconClassName"
    | "tooltipClassName"
  >
> {}

const TextFieldContext = createContext<TextFieldContextProps>({});

export interface TextFieldProps
  extends AriaTextFieldProps, TextFieldContextProps {}

export const TextField = ({
  className,
  size = "md",
  inputClassName,
  wrapperClassName,
  iconClassName,
  tooltipClassName,
  ...props
}: TextFieldProps) => {
  return (
    <TextFieldContext.Provider
      value={{
        inputClassName,
        wrapperClassName,
        iconClassName,
        tooltipClassName,
        size,
      }}
    >
      <AriaTextField
        {...props}
        data-input-wrapper
        data-input-size={size}
        className={(state) =>
          cx(
            "group flex h-max w-full flex-col items-start justify-start gap-1.5",
            typeof className === "function" ? className(state) : className,
          )
        }
      />
    </TextFieldContext.Provider>
  );
};

TextField.displayName = "TextField";

export interface InputProps
  extends
    AriaTextFieldProps,
    Pick<
      InputBaseProps,
      | "ref"
      | "placeholder"
      | "icon"
      | "shortcut"
      | "tooltip"
      | "invalidTooltip"
      | "groupRef"
      | "size"
      | "wrapperClassName"
      | "inputClassName"
      | "iconClassName"
      | "tooltipClassName"
    > {
  /** Label text for the input */
  label?: string;
  /** Native input ID for an external label. */
  inputId?: string;
  /** Helper text displayed below the input */
  hint?: ReactNode;
  /** Whether to hide required indicator from label */
  hideRequiredIndicator?: boolean;
}

export const Input = ({
  size = "md",
  placeholder,
  icon: Icon,
  label,
  inputId,
  hint,
  shortcut,
  hideRequiredIndicator,
  className,
  ref,
  groupRef,
  tooltip,
  invalidTooltip,
  iconClassName,
  inputClassName,
  wrapperClassName,
  tooltipClassName,
  type = "text",
  ...props
}: InputProps) => {
  return (
    <TextField
      aria-label={!label ? placeholder : undefined}
      {...props}
      size={size}
      className={className}
    >
      {({ isRequired, isInvalid }) => (
        <>
          {label && (
            <Label
              isRequired={
                hideRequiredIndicator ? !hideRequiredIndicator : isRequired
              }
              isInvalid={isInvalid}
            >
              {label}
            </Label>
          )}

          <InputBase
            id={inputId}
            {...{
              ref,
              groupRef,
              size,
              placeholder,
              icon: Icon,
              shortcut,
              iconClassName,
              inputClassName,
              wrapperClassName,
              tooltipClassName,
              tooltip,
              invalidTooltip,
              type,
            }}
          />

          {hint && <HintText isInvalid={isInvalid}>{hint}</HintText>}
        </>
      )}
    </TextField>
  );
};

Input.displayName = "Input";
