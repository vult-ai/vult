/** Presentation only. These values never belong to a Book or a posting. */
/* `value` sets numbers (separators, currency symbol placement, grouping); `words` sets
   how dates read in the interface language: English month names in the region's
   order. Alphabetical by label, so people find their country by name. */
export const REGIONAL_FORMATS = [
  { value: "en-AU", label: "Australia", words: "en-AU" },
  { value: "de-AT", label: "Austria", words: "en-AT" },
  { value: "nl-BE", label: "Belgium (Dutch)", words: "en-BE" },
  { value: "fr-BE", label: "Belgium (French)", words: "en-BE" },
  { value: "pt-BR", label: "Brazil", words: "en-150" },
  { value: "en-CA", label: "Canada (English)", words: "en-CA" },
  { value: "fr-CA", label: "Canada (French)", words: "en-CA" },
  { value: "cs-CZ", label: "Czechia", words: "en-150" },
  { value: "da-DK", label: "Denmark", words: "en-DK" },
  { value: "fi-FI", label: "Finland", words: "en-FI" },
  { value: "fr-FR", label: "France", words: "en-150" },
  { value: "de-DE", label: "Germany", words: "en-DE" },
  { value: "en-IN", label: "India", words: "en-IN" },
  { value: "en-IE", label: "Ireland", words: "en-IE" },
  { value: "it-IT", label: "Italy", words: "en-150" },
  { value: "ja-JP", label: "Japan", words: "en-150" },
  { value: "en-KE", label: "Kenya", words: "en-KE" },
  { value: "es-MX", label: "Mexico", words: "en-150" },
  { value: "nl-NL", label: "Netherlands", words: "en-NL" },
  { value: "en-NZ", label: "New Zealand", words: "en-NZ" },
  { value: "en-NG", label: "Nigeria", words: "en-NG" },
  { value: "nb-NO", label: "Norway", words: "en-150" },
  { value: "pl-PL", label: "Poland", words: "en-150" },
  { value: "pt-PT", label: "Portugal", words: "en-150" },
  { value: "en-SG", label: "Singapore", words: "en-SG" },
  { value: "en-ZA", label: "South Africa", words: "en-ZA" },
  { value: "es-ES", label: "Spain", words: "en-150" },
  { value: "sv-SE", label: "Sweden", words: "en-SE" },
  { value: "de-CH", label: "Switzerland", words: "en-CH" },
  { value: "en-AE", label: "United Arab Emirates", words: "en-AE" },
  { value: "en-GB", label: "United Kingdom", words: "en-GB" },
  { value: "en-US", label: "United States", words: "en-US" },
] as const;

/** The locale for words in dates (month names) for a regional format. */
export function wordsLocale(regional?: string) {
  return REGIONAL_FORMATS.find((r) => r.value === regional)?.words ?? "en-US";
}

export interface UserPreferences {
  version: 1;
  language: "en";
  regional_format: (typeof REGIONAL_FORMATS)[number]["value"];
  date_format: "standard" | "month-day-year" | "day-month-year" | "iso";
  time_format: "12h" | "24h";
  digit_grouping: boolean;
  negative_style: "minus" | "parentheses";
}

export const DEFAULT_USER_PREFERENCES: Readonly<UserPreferences> =
  Object.freeze({
    version: 1,
    language: "en",
    regional_format: "en-US",
    date_format: "standard",
    time_format: "24h",
    digit_grouping: true,
    negative_style: "minus",
  });

/** A verified User identity is supplied by the host, never inferred from a Book.
 * Even User-scoped values in this store are local to this browser, not synced. */
export type PreferenceScope =
  { kind: "browser" } | { kind: "user"; id: string };
export type PreferencePatch = Partial<Omit<UserPreferences, "version">>;
export type PreferenceStorage = Pick<Storage, "getItem" | "setItem">;
export type PreferencePersistence = "saved" | "unavailable" | "invalid";
export interface PreferenceSnapshot {
  readonly value: Readonly<UserPreferences>;
  readonly persistence: PreferencePersistence;
}

const fields = Object.keys(DEFAULT_USER_PREFERENCES);
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function parseUserPreferences(
  value: unknown,
): Readonly<UserPreferences> | null {
  if (
    !object(value) ||
    Object.keys(value).length !== fields.length ||
    Object.keys(value).some((key) => !fields.includes(key)) ||
    value.version !== 1 ||
    value.language !== "en" ||
    !REGIONAL_FORMATS.some((item) => item.value === value.regional_format) ||
    typeof value.date_format !== "string" ||
    !["standard", "month-day-year", "day-month-year", "iso"].includes(
      value.date_format,
    ) ||
    typeof value.time_format !== "string" ||
    !["12h", "24h"].includes(value.time_format) ||
    typeof value.digit_grouping !== "boolean" ||
    typeof value.negative_style !== "string" ||
    !["minus", "parentheses"].includes(value.negative_style)
  )
    return null;
  return Object.freeze({ ...value }) as unknown as Readonly<UserPreferences>;
}

export function browserScopeKey(scope: PreferenceScope) {
  if (scope.kind === "browser") return "browser";
  if (
    scope.kind !== "user" ||
    typeof scope.id !== "string" ||
    !scope.id.trim() ||
    scope.id.length > 256
  )
    throw new Error(
      "A verified User identity is required for User preferences.",
    );
  return `user:${encodeURIComponent(scope.id)}`;
}

export function preferenceStorageKey(scope: PreferenceScope) {
  return `vult-presentation:v1:${browserScopeKey(scope)}`;
}

export function createPreferenceStore(
  scope: PreferenceScope,
  storage: PreferenceStorage | null,
) {
  const key = preferenceStorageKey(scope);
  const owner: PreferenceScope =
    scope.kind === "user"
      ? Object.freeze({ kind: "user", id: scope.id })
      : Object.freeze({ kind: "browser" });
  const listeners = new Set<() => void>();
  let canPersist = false;
  let snapshot: Readonly<PreferenceSnapshot>;
  const result = (
    value: Readonly<UserPreferences>,
    persistence: PreferencePersistence,
  ) => Object.freeze({ value, persistence });

  function read() {
    canPersist = false;
    if (!storage) return result(DEFAULT_USER_PREFERENCES, "unavailable");
    let raw: string | null;
    try {
      raw = storage.getItem(key);
    } catch {
      return result(DEFAULT_USER_PREFERENCES, "unavailable");
    }
    try {
      if (raw === null) {
        canPersist = true;
        return result(DEFAULT_USER_PREFERENCES, "saved");
      }
      const data: unknown = JSON.parse(raw);
      if (
        !object(data) ||
        Object.keys(data).length !== 2 ||
        !object(data.owner) ||
        data.owner.kind !== owner.kind ||
        (owner.kind === "user"
          ? data.owner.id !== owner.id || Object.keys(data.owner).length !== 2
          : Object.keys(data.owner).length !== 1)
      )
        return result(DEFAULT_USER_PREFERENCES, "invalid");
      const value = parseUserPreferences(data.preferences);
      if (!value) return result(DEFAULT_USER_PREFERENCES, "invalid");
      canPersist = true;
      return result(value, "saved");
    } catch {
      // Corrupt or newer data is retained, never replaced with our defaults.
      return result(DEFAULT_USER_PREFERENCES, "invalid");
    }
  }
  snapshot = read();
  const notify = () => {
    for (const listener of listeners) listener();
  };
  return {
    key,
    owner,
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    reload() {
      snapshot = read();
      notify();
    },
    update(patch: PreferencePatch) {
      if (
        !object(patch) ||
        Object.keys(patch).some(
          (field) => field === "version" || !fields.includes(field),
        )
      )
        throw new Error("Unsupported presentation preference.");
      let value = parseUserPreferences({ ...snapshot.value, ...patch });
      if (!value) throw new Error("Unsupported presentation preference.");
      let persistence = snapshot.persistence;
      if (canPersist && storage) {
        // Another tab may have changed the envelope before its event arrives.
        const latest = read();
        if (latest.persistence !== "saved") {
          persistence = latest.persistence;
        } else {
          if (snapshot.persistence === "saved") {
            value = parseUserPreferences({ ...latest.value, ...patch });
            if (!value) throw new Error("Unsupported presentation preference.");
          }
          try {
            storage.setItem(key, JSON.stringify({ owner, preferences: value }));
            persistence = "saved";
          } catch {
            persistence = "unavailable";
          }
        }
      }
      snapshot = result(value, persistence);
      notify();
    },
  };
}

export type PreferenceStore = ReturnType<typeof createPreferenceStore>;
