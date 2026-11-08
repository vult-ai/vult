import { OnboardingChoice } from "../onboarding/onboarding-choice";
import { countryOption } from "../onboarding/country-search";
import type { LegalCatalog } from "../../lib/legal-identity";

export function IdentityCountryChoice({
  label,
  name,
  value,
  countries,
  onChange,
}: {
  label: string;
  name?: string;
  value: string;
  countries: LegalCatalog["countries"];
  onChange: (value: string) => void;
}) {
  return (
    <OnboardingChoice
      label={label}
      name={name}
      value={value}
      onChange={(code) => onChange(code === "unknown" ? "" : code)}
      placeholder="Choose a country"
      items={[
        { id: "unknown", label: "Not set" },
        ...countries.map(countryOption),
        ...(value && !countries.some((c) => c.code === value)
          ? [{ id: value, label: value }]
          : []),
      ]}
    />
  );
}
