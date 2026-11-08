"use client";

import {
  Input as BaseInput,
  InputBase as BaseInputBase,
  TextField as BaseTextField,
  type InputBaseProps,
  type InputProps,
  type TextFieldProps,
} from "@vult/components/base/input/input";

export type { InputBaseProps, InputProps, TextFieldProps };

export const InputBase = ({ size = "md", ...props }: InputBaseProps) => (
  <BaseInputBase {...props} size={size} />
);
export const TextField = ({ size = "md", ...props }: TextFieldProps) => (
  <BaseTextField {...props} size={size} />
);
export const Input = ({ size = "md", ...props }: InputProps) => (
  <BaseInput {...props} size={size} />
);
