import type { Institution } from "./shared-institutions";

export type EntryBrand =
  | { state: "identified"; brand_id: string }
  | { state: "proposed"; name: string; domain?: string }
  | { state: "unknown" | "not_applicable" };

const key = (value: string) =>
  value.normalize("NFKC").trim().replace(/\s+/gu, " ").toLowerCase();

/** Match typed public identities only. Financial descriptions never select logos. */
export function activityBrand(
  brand: EntryBrand | undefined,
  catalog: readonly Institution[],
) {
  if (brand?.state === "identified")
    return catalog.find((item) => item.id === brand.brand_id);
  if (brand?.state !== "proposed") return undefined;
  const matches = catalog.filter(
    (item) =>
      [item.name, ...(item.aliases || [])].some(
        (name) => key(name) === key(brand.name),
      ) &&
      (!brand.domain || key(brand.domain) === key(item.domain)),
  );
  return matches.length === 1 ? matches[0] : undefined;
}

export function parseEntryBrands(value: unknown, ids: readonly string[]) {
  const rows = (value as { brands?: unknown })?.brands;
  if (!Array.isArray(rows) || rows.length !== ids.length)
    throw new Error("Invalid entry brand response");
  const requested = new Set(ids),
    result = new Map<string, EntryBrand>();
  for (const row of rows) {
    if (!row || !requested.has(row.entry_id) || result.has(row.entry_id))
      throw new Error("Invalid entry brand identity");
    if (
      (row.state === "identified" &&
        typeof row.brand_id === "string" &&
        /^[a-z][a-z0-9-]{0,63}$/.test(row.brand_id)) ||
      (row.state === "proposed" &&
        typeof row.name === "string" &&
        row.name.trim() &&
        [...row.name].length <= 100 &&
        (row.domain === undefined || typeof row.domain === "string")) ||
      row.state === "unknown" ||
      row.state === "not_applicable"
    )
      result.set(row.entry_id, row as EntryBrand);
    else throw new Error("Invalid entry brand state");
  }
  return result;
}

/** Book-private batches use the application origin, never the public asset service. */
