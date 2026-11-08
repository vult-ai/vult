import { parseUserProfile, profileStorageKey, type UserProfile } from "./user-profile";
import { parseUserPreferences, preferenceStorageKey, type PreferenceStorage } from "./user-preferences";
import { THEME_STORAGE_KEY, normalizeThemePreference } from "../design-system/theme-preference";
import type { AccountPatch, AccountSettings } from "./account-settings";

export type LegacyPersonalData = {
  fingerprint: string;
  patch: AccountPatch;
  profile: Readonly<UserProfile> | null;
  warning: boolean;
};
const ownerIsBrowser = (value: unknown): value is { owner: { kind: "browser" } } => {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const owner = (value as { owner?: unknown }).owner;
  return !!owner && typeof owner === "object" && !Array.isArray(owner) &&
    Object.keys(owner).length === 1 && (owner as { kind?: unknown }).kind === "browser";
};
/** An untrusted legacy copy is a proposal only. Reading never adopts or deletes it. */
export async function readLegacyPersonalData(storage: PreferenceStorage | null): Promise<LegacyPersonalData | null> {
  if (!storage) return null;
  let profileRaw: string | null, preferencesRaw: string | null, themeRaw: string | null;
  try {
    profileRaw = storage.getItem(profileStorageKey({ kind: "browser" }));
    preferencesRaw = storage.getItem(preferenceStorageKey({ kind: "browser" }));
    themeRaw = storage.getItem(THEME_STORAGE_KEY);
  } catch { return null; }
  if (profileRaw === null && preferencesRaw === null && themeRaw === null) return null;
  const patch: AccountPatch = {};
  let profile: Readonly<UserProfile> | null = null, warning = false;
  if (profileRaw !== null) {
    try {
      if (profileRaw.length > 205000) throw Error();
      const envelope: unknown = JSON.parse(profileRaw);
      if (!ownerIsBrowser(envelope) || Object.keys(envelope).length !== 2) throw Error();
      profile = parseUserProfile((envelope as unknown as { profile: unknown }).profile);
      if (!profile || (profile.photo !== null && !profile.photo.startsWith("data:image/jpeg;base64,"))) throw Error();
      patch.profile = { display_name: profile.display_name, photo: profile.photo };
    } catch { profile = null; warning = true; }
  }
  if (preferencesRaw !== null) {
    try {
      if (preferencesRaw.length > 4096) throw Error();
      const envelope: unknown = JSON.parse(preferencesRaw);
      if (!ownerIsBrowser(envelope) || Object.keys(envelope).length !== 2) throw Error();
      const preferences = parseUserPreferences((envelope as unknown as { preferences: unknown }).preferences);
      if (!preferences) throw Error();
      const { version: _version, ...values } = preferences;
      patch.preferences = values;
    } catch { warning = true; }
  }
  if (themeRaw !== null) {
    const appearance = normalizeThemePreference(themeRaw);
    if (appearance) patch.appearance = appearance;
    else warning = true;
  }
  // This digest identifies a device prompt; it grants no User identity or rights.
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify([profileRaw, preferencesRaw, themeRaw])));
  const fingerprint = Array.from(new Uint8Array(digest), n => n.toString(16).padStart(2, "0")).join("");
  return { fingerprint, patch, profile, warning };
}
export const legacyChoiceKey = (user: string) => {
  if (!/^[0-9a-f]{64}$/.test(user)) throw Error("Verified User required.");
  return `vult-personal-import:v1:${user}`;
};
export function legacyChoiceRemembered(storage: PreferenceStorage | null, user: string, copy: LegacyPersonalData) {
  try { return storage?.getItem(legacyChoiceKey(user)) === copy.fingerprint; } catch { return false; }
}
export function rememberLegacyChoice(storage: PreferenceStorage | null, user: string, copy: LegacyPersonalData) {
  try { storage?.setItem(legacyChoiceKey(user), copy.fingerprint); } catch { /* Device acknowledgement only; durable data is unaffected. */ }
}
export function legacyDiffers(copy: LegacyPersonalData, saved: AccountSettings) {
  if (copy.warning) return true;
  const p = copy.patch;
  return (p.profile && Object.entries(p.profile).some(([key, value]) => saved.profile[key as keyof UserProfile] !== value)) ||
    (p.preferences && Object.entries(p.preferences).some(([key, value]) => saved.preferences[key as keyof AccountSettings["preferences"]] !== value)) ||
    (p.appearance !== undefined && p.appearance !== saved.appearance) || false;
}
export function legacyImportMatches(copy: LegacyPersonalData, saved: AccountSettings) {
  const p = copy.patch;
  return (!p.profile || (p.profile.display_name === saved.profile.display_name &&
    (p.profile.photo === null ? saved.profile.photo === null : !!saved.profile.photo && saved.profile.photo.startsWith("/api/v1/me/avatar/")))) &&
    (!p.preferences || Object.entries(p.preferences).every(([key, value]) => saved.preferences[key as keyof AccountSettings["preferences"]] === value)) &&
    (p.appearance === undefined || p.appearance === saved.appearance);
}
