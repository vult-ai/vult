import type { Currency } from "../../lib/api";
import { currencyTerritories } from "./currency-territories";

const names = new Intl.DisplayNames(["en"], { type: "currency" });

export function normalizeCurrencySearch(value: string) {
  return value
    .toLocaleLowerCase("en")
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .replace(/ł/g, "l")
    .replace(/[^\p{L}\p{N}\p{Sc}]+/gu, " ")
    .trim();
}

export function matchesCurrencySearch(terms: string, query: string) {
  return normalizeCurrencySearch(query)
    .split(/\s+/)
    .filter(Boolean)
    .every((word) => terms.includes(word));
}
