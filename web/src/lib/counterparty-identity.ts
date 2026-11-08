import type { EntryBrand } from "./activity-brands";
import type { Institution } from "./shared-institutions";

/** Explicit approved identity links. Descriptions and partial matches are never
 * evidence of a counterparty. New brand links must reference catalog IDs.
 */
const brandLinks: Readonly<Record<string, string>> = Object.freeze({
  wise: "wise",
  mercury: "mercury",
});

function identityName(value: string | null | undefined) {
  return (value || "")
    .normalize("NFKC")
    .trim()
    .replace(/\s+/gu, " ");
}

export function counterpartyIdentity(counterparty: string | null | undefined) {
  const name = identityName(counterparty);
  const normalized = name.toLowerCase();
  return {
    name,
    key: normalized ? `counterparty:${normalized}` : "counterparty:unknown",
    institutionId: Object.hasOwn(brandLinks, normalized)
      ? brandLinks[normalized]
      : undefined,
  };
}

/** Display-only fallback; no description parsing or brand creation. */
export function counterpartyAvatarIdentity({
  brand,
  institution,
  counterparty,
}: {
  brand?: EntryBrand;
  institution?: Pick<Institution, "id" | "name">;
  counterparty?: string | null;
}) {
  if (institution)
    return { name: institution.name, key: `brand:${institution.id}` };
  if (brand?.state === "proposed") {
    const name = identityName(brand.name);
    if (name) return { name, key: `brand:proposed:${name.toLowerCase()}` };
  }
  const { name, key } = counterpartyIdentity(counterparty);
  return { name, key };
}
