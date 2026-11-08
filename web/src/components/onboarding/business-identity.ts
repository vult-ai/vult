import type {
  LegalCatalog,
  LegalIdentityInput,
} from "../../lib/legal-identity";
export type BusinessIdentityDraft = {
  registrationStatus: "" | "registered" | "not_registered" | "unknown";
  country: string;
  jurisdiction: string;
  customJurisdiction: string;
  form: string;
  customForm: string;
  tax: string;
  overlay: string;
  legalName: string;
  authority: string;
  registration: string;
  registrationCountry: string;
};
export function businessJurisdictions(catalog: LegalCatalog, country: string) {
  return catalog.jurisdictions.filter((item) => item.country_code === country);
}
export function nationalJurisdiction(catalog: LegalCatalog, country: string) {
  const choices = businessJurisdictions(catalog, country);
  return choices.length === 1 && choices[0].id === country
    ? country
    : undefined;
}
export function registrationCountry(draft: BusinessIdentityDraft) {
  return draft.registrationStatus === "not_registered"
    ? "unregistered"
    : draft.registrationStatus === "unknown"
      ? "unknown"
      : draft.country;
}
