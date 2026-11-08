"use client";

import { useLayoutEffect } from "react";
import { useFocusVisible } from "react-aria";
import { installOverlayFocusGuard } from "./overlay-focus-guard";

export function FocusVisibility() {
  // Reuse React Aria's mouse, touch, keyboard and assistive-navigation tracking.
  // Typing in a mouse-focused text field does not enable keyboard focus rings.
  const { isFocusVisible } = useFocusVisible();

  useLayoutEffect(() => installOverlayFocusGuard(document), []);

  useLayoutEffect(() => {
    document.documentElement.dataset.vultFocusVisible = String(isFocusVisible);
    return () => {
      delete document.documentElement.dataset.vultFocusVisible;
    };
  }, [isFocusVisible]);

  return null;
}
