import { AmountPrivacyToggle } from "../../src/components/ui/amount-privacy-toggle";
import { hiddenAmount, useAmountsHidden } from "../../src/lib/amount-privacy";

/** The toggle beside one figure, sharing one copy of the setting. */
export function PrivacyExample() {
  return (
    <>
      <AmountPrivacyToggle />
      <output>{useAmountsHidden() ? hiddenAmount : "$1,250.00"}</output>
    </>
  );
}
export { hiddenAmount };
