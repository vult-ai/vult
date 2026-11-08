"use client";

import type { ComponentProps } from "react";
import { CloseButton as BaseCloseButton } from "@vult/components/base/buttons/close-button";

export const CloseButton = ({ size = "xs", ...props }: ComponentProps<typeof BaseCloseButton>) => (
    <BaseCloseButton {...props} size={size} />
);
