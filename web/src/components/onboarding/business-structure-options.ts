import type { LegalForm } from "../../lib/legal-identity";
import type { OnboardingOption } from "./onboarding-choice";
import { normalizeCurrencySearch } from "./currency-search";
import profileData from "./business-structure-profiles.json";

type ProfileChoice = {
  key: string;
  label: string;
  elf_codes: string[];
  ids: string[];
  exclude_jurisdictions?: string[];
};
type CountryProfile = {
  source: string;
  choices: ProfileChoice[];
  exclude_jurisdictions?: string[];
};
export type StructureChoice = OnboardingOption & {
  form: string;
  customForm: string;
};
const profiles: Record<string, CountryProfile> = profileData.countries;

function catalogChoice(form: LegalForm): StructureChoice {
  return {
    id: form.id,
    label: form.local_name,
    search: normalizeCurrencySearch(
      [
        form.local_name,
        form.english_name,
        ...(form.aliases ?? []),
        form.elf_code ?? "",
      ].join(" "),
    ),
    form: form.id,
    customForm: "",
  };
}

/** The supplied forms must already be scoped to the selected jurisdiction.
 * A broad or missing match remains an explicit owner-reported label, rather
 * than selecting an arbitrary registry ID or inferring a tax/subject type. */
export function businessStructureOptions(
  forms: LegalForm[],
  country: string,
  jurisdiction: string,
  form: string,
  customForm: string,
) {
  const profile = profiles[country];
  const definitions = profile?.exclude_jurisdictions?.includes(jurisdiction)
    ? []
    : (profile?.choices ?? []).filter(
        (choice) => !choice.exclude_jurisdictions?.includes(jurisdiction),
      );
  const common: StructureChoice[] = definitions.map((choice) => {
    const matches = forms.filter(
      (form) =>
        form.country_code === country &&
        (choice.ids.includes(form.id) ||
          (form.elf_code && choice.elf_codes.includes(form.elf_code))),
    );
    const exact = matches.length === 1 ? matches[0] : undefined;
    return {
      ...(exact ? catalogChoice(exact) : {}),
      id: exact?.id ?? `common:${country}:${choice.key}`,
      label: choice.label,
      search: normalizeCurrencySearch(
        `${choice.label} ${exact ? catalogChoice(exact).search : ""}`,
      ),
      form: exact?.id ?? "custom",
      customForm: exact ? "" : choice.label,
    };
  });
  const selected = common.find(
    (choice) =>
      choice.form === form &&
      (form !== "custom" || choice.customForm === customForm),
  );
  // An older/specialized choice stays visible and keeps its exact identity.
  const retained = forms.find((item) => item.id === form);
  if (retained && !common.some((item) => item.id === retained.id))
    common.push(catalogChoice(retained));
  const all = [...common];
  for (const item of forms)
    if (!all.some((choice) => choice.id === item.id))
      all.push(catalogChoice(item));
  const fallback: StructureChoice[] = [
    {
      id: "custom",
      label: definitions.length ? "Another structure" : "Enter a structure",
      form: "custom",
      customForm: "",
    },
    { id: "unknown", label: "Not sure yet", form: "unknown", customForm: "" },
  ];
  return {
    common: [...common, ...fallback],
    all: [...all, ...fallback],
    value: selected?.id ?? form,
    hasMore: all.length > common.length,
    reviewed: definitions.length > 0,
  };
}
