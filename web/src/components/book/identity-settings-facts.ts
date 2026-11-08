import type {
  LegalCatalog,
  LegalIdentity,
  LegalIdentityInput,
} from "../../lib/legal-identity";

const kinds: Record<string, string> = {
  person: "Person",
  household: "Household",
  legal_entity: "Business or organization",
  trust: "Trust",
  estate: "Estate",
  fund: "Fund",
  informal_activity: "Informal activity",
  other: "Other",
};

export function identitySettingsFacts(
  kind: string | null,
  identity: LegalIdentityInput,
  catalog: LegalCatalog,
) {
  const enriched = identity as LegalIdentity;
  const country = (code: string) =>
    catalog.countries.find((c) => c.code === code)?.name || code;
  const names = (ids: string[] = [], choices: { id: string; name: string }[]) =>
    ids.map((id) => choices.find((c) => c.id === id)?.name || id);
  return [
    {
      key: "name",
      label: "Legal name",
      raw: identity.legal_name || "",
      value: identity.legal_name || "",
    },
    {
      key: "kind",
      label: "Represents",
      raw: kind || "",
      value: kinds[kind || ""] || kind || "",
    },
    {
      key: "country",
      label: "Country",
      raw: identity.formation_or_governing_country_code || "",
      value: identity.formation_or_governing_country_code
        ? country(identity.formation_or_governing_country_code)
        : "",
    },
    {
      key: "jurisdiction",
      label: "Jurisdiction",
      raw: [
        identity.legal_jurisdiction_id || "",
        identity.custom_legal_jurisdiction || "",
      ],
      value:
        catalog.jurisdictions.find(
          (j) => j.id === identity.legal_jurisdiction_id,
        )?.name ||
        enriched.jurisdiction?.name ||
        identity.custom_legal_jurisdiction ||
        identity.legal_jurisdiction_id ||
        "",
    },
    {
      key: "structure",
      label: "Business structure",
      raw: [identity.legal_form_id || "", identity.custom_legal_form || ""],
      value:
        catalog.forms.find((f) => f.id === identity.legal_form_id)
          ?.local_name ||
        enriched.legal_form?.local_name ||
        identity.custom_legal_form ||
        identity.legal_form_id ||
        "",
    },
    {
      key: "status",
      label: "Registration",
      raw: identity.registration_status || "",
      value:
        {
          registered: "Registered",
          not_registered: "Not formally registered",
          unknown: "Not sure",
        }[identity.registration_status || ("" as "registered")] || "",
    },
    {
      key: "designations",
      label: "Designations",
      raw: [identity.overlay_ids || [], identity.custom_overlays || []],
      value: [
        ...names(identity.overlay_ids, catalog.overlays),
        ...(identity.custom_overlays || []),
      ].join(", "),
    },
    {
      key: "tax",
      label: "Tax treatment",
      raw: [
        identity.tax_classification_ids || [],
        identity.custom_tax_classifications || [],
      ],
      value: [
        ...names(identity.tax_classification_ids, catalog.tax_classifications),
        ...(identity.custom_tax_classifications || []),
      ].join(", "),
    },
    {
      key: "residences",
      label: "Tax residence",
      raw: identity.tax_residence_country_codes || [],
      value:
        identity.tax_residence_country_codes?.map(country).join(", ") || "",
    },
    {
      key: "registrations",
      label: "Registrations",
      raw: identity.registrations || [],
      value:
        identity.registrations
          ?.map((r) =>
            [
              r.authority,
              r.number,
              r.country_code ? country(r.country_code) : "",
              r.label,
            ]
              .filter(Boolean)
              .join(" · "),
          )
          .join("; ") || "",
    },
  ];
}

export function identitySettingsChanges(
  priorKind: string | null,
  prior: LegalIdentityInput,
  nextKind: string | null,
  next: LegalIdentityInput,
  catalog: LegalCatalog,
) {
  const before = identitySettingsFacts(priorKind, prior, catalog);
  return identitySettingsFacts(nextKind, next, catalog).flatMap((row, i) =>
    JSON.stringify(row.raw) === JSON.stringify(before[i].raw)
      ? []
      : [
          {
            ...row,
            before: before[i].value || "Not set",
            value: row.value || "Not set",
          },
        ],
  );
}
