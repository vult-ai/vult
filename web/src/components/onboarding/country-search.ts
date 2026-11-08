import { normalizeCurrencySearch } from "./currency-search";
import type { OnboardingOption } from "./onboarding-choice";

const regionNames = new Intl.DisplayNames(["en"], { type: "region" });
const aliases: Record<string, { codes: string[]; names?: string[] }> = {
  US: { codes: ["USA"], names: ["United States of America"] },
  GB: { codes: ["UK", "GBR"], names: ["Britain", "Great Britain"] },
  TR: { codes: ["TUR"], names: ["Turkey", "Turkiye"] },
};

export function countryOption(country: { code: string; name: string }): OnboardingOption {
  const alternative = aliases[country.code];
  return {
    id: country.code,
    label: country.name,
    searchCodes: alternative?.codes,
    search: normalizeCurrencySearch([
      country.code,
      country.name,
      regionNames.of(country.code) ?? "",
      ...(alternative?.codes ?? []),
      ...(alternative?.names ?? []),
    ].join(" ")),
  };
}
