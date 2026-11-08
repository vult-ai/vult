import { RollingNumber } from "./rolling-number";
import { amountFraction } from "../../lib/amount-fraction";

/** A display-size exact amount: the whole part at full size and the fraction,
 * its decimal separator and digits, smaller on the same baseline, so dollars
 * read first. Signs, symbols and separators stay full size. It is one string
 * for selection, copying and screen readers. Tables keep plain amounts, whose
 * digits must line up. */
export function DisplayAmount({ value, className = "" }: { value: string; className?: string }) {
  return <RollingNumber value={value} className={className} fraction={amountFraction(value)} />;
}
