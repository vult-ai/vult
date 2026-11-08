export type LegalIdentityInput = {
  registration_status?: "registered" | "not_registered" | "unknown";
  legal_name?: string;
  formation_or_governing_country_code?: string;
  legal_jurisdiction_id?: string;
  custom_legal_jurisdiction?: string;
  legal_form_id?: string;
  custom_legal_form?: string;
  overlay_ids?: string[];
  custom_overlays?: string[];
  tax_classification_ids?: string[];
  custom_tax_classifications?: string[];
  tax_residence_country_codes?: string[];
  registrations?: {
    country_code?: string;
    authority: string;
    number: string;
    label?: string;
  }[];
};
type Source = { title: string; url: string; checked_date: string };
export type LegalForm = {
  id: string;
  elf_code?: string;
  country_code: string;
  jurisdiction_ids: string[];
  local_name: string;
  english_name: string;
  aliases?: string[];
  legal_personality: boolean | null;
  sources: Source[];
};
export type LegalIdentity = LegalIdentityInput & {
  catalog_version?: string;
  status?: "unknown" | "custom" | "verified";
  country?: { code: string; name: string };
  jurisdiction?: { id: string; name: string };
  legal_form?: LegalForm;
  overlays?: { id: string; name: string }[];
  tax_classifications?: { id: string; name: string }[];
};
export type AccountingSubject = {
  id: string;
  kind: string | null;
  revision: number;
  identity: LegalIdentity;
};
export type LegalCatalog = {
  version: string;
  checked_date: string;
  countries: { code: string; name: string }[];
  jurisdictions: { id: string; country_code: string; name: string }[];
  forms: LegalForm[];
  overlays: {
    id: string;
    country_code: string;
    name: string;
    legal_form_ids: string[];
  }[];
  tax_classifications: { id: string; country_code: string; name: string }[];
};
export type IdentityHistory = {
  id: string;
  revision: number;
  prior_kind: string | null;
  new_kind: string | null;
  prior_identity: LegalIdentity;
  new_identity: LegalIdentity;
  actor: string;
  reason: string;
  effective_date: string | null;
  recorded_at: string;
};
export type IdentityView = {
  subject: AccountingSubject;
  history: IdentityHistory[];
  has_more: boolean;
  next_offset: number;
};
export function identityLabel(identity: LegalIdentity) {
  return (
    identity.legal_form?.local_name ||
    identity.custom_legal_form ||
    "Not classified"
  );
}
