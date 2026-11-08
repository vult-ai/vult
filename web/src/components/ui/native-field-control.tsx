import {
  Children,
  isValidElement,
  type InputHTMLAttributes,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
  type ReactElement,
  type ReactNode,
} from "react";
import {
  InputBase,
  type InputBaseProps,
} from "../../design-system/catalog/components/vult-ui/input";
import { Select } from "./select";
import { FileInput } from "../../design-system/catalog/components/vult-ui/file-input";
import { TextAreaBase } from "../../design-system/catalog/components/vult-ui/textarea";
import { cn } from "../../lib/utils";

/** Canonical presentation with unchanged native names, events, dates and FileLists. */
export function NativeFieldControl({
  children,
  hint,
}: {
  children: ReactElement;
  hint?: string;
}) {
  if (children.type === "select") {
    const {
      children: options,
      className,
      size: _size,
      ...props
    } = children.props as SelectHTMLAttributes<HTMLSelectElement>;
    return (
      <Select
        {...props}
        hint={hint}
        selectClassName={cn("min-w-0 w-full", className)}
        options={Children.toArray(options)
          .filter(isValidElement)
          .map((option) => {
            const item = option.props as {
              value?: string | number;
              disabled?: boolean;
              children: ReactNode;
            };
            const label = Children.toArray(item.children).join("");
            return {
              value: String(item.value ?? label),
              label,
              disabled: item.disabled,
            };
          })}
      />
    );
  }
  if (children.type === "textarea") {
    return (
      <TextAreaBase
        {...(children.props as TextareaHTMLAttributes<HTMLTextAreaElement>)}
      />
    );
  }
  if (children.type !== "input") return children;
  const props = children.props as InputHTMLAttributes<HTMLInputElement>;
  if (props.type === "file") {
    const {
      type: _type,
      value: _value,
      defaultValue: _default,
      size: _size,
      ...fileProps
    } = props;
    return <FileInput {...fileProps} />;
  }
  if (props.type === "checkbox" || props.type === "radio") return children;
  const { size: _size, className, required, disabled, ...inputProps } = props;
  return (
    <InputBase
      {...(inputProps as InputBaseProps)}
      isRequired={required}
      isDisabled={disabled}
      disabled={disabled}
      inputClassName={className}
    />
  );
}
