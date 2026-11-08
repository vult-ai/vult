import { useMemo, type ReactNode } from "react";
import { ComboBox } from "../ui/combobox";
import {
  matchesCurrencySearch,
  normalizeCurrencySearch,
} from "./currency-search";

export type OnboardingOption = {
  id: string;
  label: string;
  search?: string;
  searchCodes?: string[];
  onAction?: () => void;
};

export function OnboardingChoice({
  label,
  name,
  value,
  items,
  onChange,
  placeholder = "Search",
  className = "",
  labelAction,
}: {
  label: string;
  name?: string;
  value: string;
  items: OnboardingOption[];
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  labelAction?: ReactNode;
}) {
  const index = useMemo(() => {
    const codes = new Map<string, Set<string>>();
    for (const item of items) {
      for (const code of [item.id, ...(item.searchCodes ?? [])]) {
        const key = normalizeCurrencySearch(code);
        const matches = codes.get(key) ?? new Set<string>();
        matches.add(item.label);
        codes.set(key, matches);
      }
    }
    return {
      terms: new Map(
        items.map((item) => [
          item.label,
          item.search ?? normalizeCurrencySearch(item.label),
        ]),
      ),
      codes,
      actions: new Set(
        items.filter((item) => item.onAction).map((item) => item.label),
      ),
    };
  }, [items]);
  return (
    <ComboBox
      label={label}
      name={name}
      value={value}
      items={items}
      onChange={onChange}
      placeholder={placeholder}
      className={`field onboarding-choice ${className}`}
      labelAction={labelAction}
      defaultFilter={(text, search) => {
        if (index.actions.has(text)) return true;
        const exact = index.codes.get(normalizeCurrencySearch(search));
        if (exact) return exact.has(text);
        if (
          ["custom", "unknown", "unregistered"].some((id) =>
            index.codes.get(id)?.has(text),
          )
        )
          return true;
        return matchesCurrencySearch(
          index.terms.get(text) ?? normalizeCurrencySearch(text),
          search,
        );
      }}
    />
  );
}
