"use client";

import type { ComponentProps } from "react";
import {
  TextArea as BaseTextArea,
  TextAreaBase as BaseTextAreaBase,
} from "@vult/components/base/textarea/textarea";

export const TextAreaBase = ({
  size = "sm",
  ...props
}: ComponentProps<typeof BaseTextAreaBase>) => (
  <BaseTextAreaBase {...props} size={size} />
);
export const TextArea = ({
  size = "sm",
  ...props
}: ComponentProps<typeof BaseTextArea>) => (
  <BaseTextArea {...props} size={size} />
);
