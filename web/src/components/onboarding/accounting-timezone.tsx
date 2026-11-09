import { useMemo } from "react";
import { OnboardingChoice } from "./onboarding-choice";
import { normalizeCurrencySearch } from "./currency-search";
import timezoneSearchAliases from "./timezone-search-aliases.json";

// Search-only IANA seasonal abbreviations and Intl English generic names (2026).
// An abbreviation can match multiple locations; selection always saves an IANA ID.
const aliases: Readonly<Record<string, readonly string[]>> = timezoneSearchAliases;

export function timezoneOptions(current: string) {
  const zones = new Set([
    "UTC",
    ...Intl.supportedValuesOf("timeZone"),
    current,
  ]);
  return [...zones]
    .filter(Boolean)
    .map((id) => {
      const parts = id.split("/").map((part) => part.replaceAll("_", " "));
      const city = parts.pop()!;
      const label =
        id === "UTC"
          ? "UTC — Coordinated Universal Time"
          : parts.length
            ? `${city} · ${parts.join(" / ")}`
            : city;
      return {
        id,
        label,
        searchCodes: aliases[id] ? [...aliases[id]] : [],
        search: normalizeCurrencySearch(
          `${id} ${label} ${aliases[id]?.join(" ") ?? ""}${id === "UTC" ? " GMT Greenwich" : ""}`,
        ),
      };
    })
    .sort((a, b) =>
      a.id === "UTC" ? -1 : b.id === "UTC" ? 1 : a.label.localeCompare(b.label),
    );
}

export function AccountingTimezone({
  value,
  onChange,
  label = "Timezone",
}: {
  value: string;
  onChange: (zone: string) => void;
  label?: string;
}) {
  const items = useMemo(() => timezoneOptions(value), [value]);
  return (
    <OnboardingChoice
      label={label}
      name="accounting_timezone"
      value={value}
      items={items}
      onChange={onChange}
      placeholder="Search city or timezone"
    />
  );
}
