import type { InputHTMLAttributes } from "react";
import { useFocusRing, mergeProps } from "react-aria";
import { CheckboxBase } from "../../design-system/catalog/components/base/checkbox/checkbox";

/** Retain the native change event while sharing the source checkbox indicator. */
export function NativeCheckbox(
  props: Omit<
    InputHTMLAttributes<HTMLInputElement>,
    "defaultChecked" | "checked" | "type"
  > & { checked: boolean; type?: "checkbox" },
) {
  const { focusProps, isFocusVisible } = useFocusRing();
  return (
    <span className="native-checkbox">
      <input
        {...mergeProps(focusProps, props)}
        type="checkbox"
        className="native-checkbox-input"
      />
      <CheckboxBase
        isSelected={Boolean(props.checked)}
        isDisabled={props.disabled}
        isFocusVisible={isFocusVisible}
      />
    </span>
  );
}
