import type { Book } from "./api";
import { displayPreferences } from "./display-preferences";

/** A rounded amount for a figure beside an exact total: "$50m". The exact
 * amount stays in the accessible label and in the exact views. */
export function compactMoney(minor: string | number | bigint, book: Pick<Book, "currency" | "scale">, locale = displayPreferences()?.regional_format || "en-US") {
  const major = Number(minor) / 10 ** book.scale;
  // Below a thousand nothing is gained by rounding, and a few cents must never read as zero.
  if (Math.abs(major) < 1000)
    return new Intl.NumberFormat(locale, { style: "currency", currency: book.currency, minimumFractionDigits: major % 1 ? book.scale : 0, maximumFractionDigits: book.scale }).format(major);
  const text = new Intl.NumberFormat(locale, { style: "currency", currency: book.currency, notation: "compact", minimumFractionDigits: 0, maximumFractionDigits: 1 })
    .format(major);
  return locale.startsWith("en") ? text.toLowerCase() : text;
}
