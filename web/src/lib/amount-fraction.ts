import { displayPreferences } from "./display-preferences";

const decimals = new Map<string, string>();
/** The decimal separator of the regional format money is shown in. */
export function decimalSeparator(locale = displayPreferences()?.regional_format || "en-US") {
  let value = decimals.get(locale);
  if (value === undefined) {
    value = new Intl.NumberFormat(locale).formatToParts(1.5).find((part) => part.type === "decimal")?.value || ".";
    decimals.set(locale, value);
  }
  return value;
}

/** Where an exact formatted amount's fraction is: the decimal separator and
 * the digits after it, as [start, end) in the string, or null when it has
 * none (a currency without minor units, or "—"). A currency symbol, sign or
 * parenthesis after it is not part of the fraction. Only for exact amounts:
 * a compact "$1.2M" or a percentage is not an amount with cents. */
export function amountFraction(text: string, decimal = decimalSeparator()): [number, number] | null {
  const start = text.lastIndexOf(decimal);
  if (start < 1 || !/\d/.test(text.slice(0, start))) return null;
  let end = start + decimal.length;
  while (end < text.length && /\d/.test(text[end])) end++;
  if (end === start + decimal.length || /\d/.test(text.slice(end))) return null;
  return [start, end];
}
