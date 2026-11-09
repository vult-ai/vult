import { Eye, EyeOff } from "../../design-system/icons";
import { setAmountsHidden, useAmountsHidden } from "../../lib/amount-privacy";
import "./amount-privacy-toggle.css";

/** Shows or hides the Dashboard's amounts: a quiet word in the Summary
 * header, where the figures are. */
export function AmountPrivacyToggle() {
  const hidden = useAmountsHidden();
  const Icon = hidden ? Eye : EyeOff;
  return (
    <button
      type="button"
      className="amount-privacy-toggle vult-type-metadata vult-focus-target"
      aria-pressed={!hidden}
      onClick={() => setAmountsHidden(!hidden)}
    >
      <Icon aria-hidden />
      {hidden ? "Show amounts" : "Hide amounts"}
    </button>
  );
}

/** A hidden figure: selecting it shows the amounts. */
export function HiddenFigure({ children }: { children: string }) {
  return (
    <button
      type="button"
      className="hidden-figure vult-focus-target"
      aria-label="Show amounts"
      title="Show amounts"
      onClick={() => setAmountsHidden(false)}
    >
      {children}
    </button>
  );
}
