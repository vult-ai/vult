import { APIError, fetchResponse, readResponse } from "./errors";
import { CSRF_HEADER } from "./session";
import {
  EMPTY_USER_PROFILE,
  parseUserProfile,
  type UserProfile,
} from "./user-profile";
import {
  DEFAULT_USER_PREFERENCES,
  parseUserPreferences,
  type PreferencePatch,
  type UserPreferences,
} from "./user-preferences";
import type { ThemePreference } from "../design-system/theme-preference";

export type AccountSettings = {
  user_id: string;
  revision: string;
  profile: Readonly<UserProfile>;
  preferences: Readonly<UserPreferences>;
  appearance: ThemePreference;
};
export type AccountPatch = {
  profile?: Partial<Pick<UserProfile, "display_name" | "photo">>;
  preferences?: PreferencePatch;
  appearance?: ThemePreference;
};
export type AccountSnapshot = {
  value: AccountSettings;
  status: "loading" | "saved" | "saving" | "error" | "conflict" | "signed-out";
  error: string;
  loaded: boolean;
  acknowledged?: number;
};
export function parseAccountSettings(
  raw: unknown,
  id: string,
): AccountSettings {
  const v = raw as AccountSettings;
  if (!v || v.user_id !== id)
    throw new APIError("Personal settings User changed. Sign in again.", 403);
  if (
    typeof v.revision !== "string" ||
    !/^(0|[1-9][0-9]{0,18})$/.test(v.revision) ||
    !["system", "light", "dark"].includes(v.appearance)
  )
    throw Error("Personal settings response could not be verified.");
  const payload = v.profile as UserProfile & { photo_asset?: unknown };
  let photo = payload?.photo;
  if (payload?.photo_asset !== undefined) {
    const asset = payload.photo_asset as Record<string, unknown>;
    if (!asset || typeof asset !== "object" || Array.isArray(asset) ||
      Object.keys(asset).length !== 4 || Object.keys(payload).length !== 4 || payload.photo !== null ||
      typeof asset.id !== "string" || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(asset.id) ||
      typeof asset.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(asset.sha256) ||
      asset.content_type !== "image/jpeg" || !Number.isInteger(asset.size) ||
      (asset.size as number) < 1 || (asset.size as number) > 147456)
      throw Error("Personal photo reference could not be verified.");
    photo = `/api/v1/me/avatar/${asset.id}?revision=${asset.sha256}`;
  }
  const profile = parseUserProfile(payload?.photo_asset !== undefined
      ? { version: payload.version, display_name: payload.display_name, photo }
      : payload),
    preferences = parseUserPreferences(v.preferences);
  if (!profile || !preferences)
    throw Error("Personal settings response could not be read.");
  return {
    user_id: id,
    revision: v.revision,
    profile,
    preferences,
    appearance: v.appearance,
  };
}
export interface AccountSettingsAPI {
  load(signal: AbortSignal): Promise<unknown>;
  save(
    patch: AccountPatch & { expected_revision: string },
    signal: AbortSignal,
  ): Promise<unknown>;
}
export function accountSettingsAPI(csrf: string): AccountSettingsAPI {
  async function request(patch: unknown, signal: AbortSignal) {
    const response = await fetchResponse("/api/v1/me/settings", {
      cache: "no-store",
      credentials: "same-origin",
      signal,
      ...(patch
        ? {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              [CSRF_HEADER]: csrf,
            },
            body: JSON.stringify(patch),
          }
        : {}),
    });
    return readResponse(response, patch ? "write" : "read");
  }
  return {
    load: (signal) => request(null, signal),
    save: (patch, signal) => request(patch, signal),
  };
}
function merge(a: AccountPatch, b: AccountPatch): AccountPatch {
  return {
    ...a,
    ...b,
    ...(a.profile || b.profile
      ? { profile: { ...a.profile, ...b.profile } }
      : {}),
    ...(a.preferences || b.preferences
      ? { preferences: { ...a.preferences, ...b.preferences } }
      : {}),
  };
}
function apply(s: AccountSettings, p: AccountPatch): AccountSettings {
  if (
    Object.keys(p).some(
      (k) => !["profile", "preferences", "appearance"].includes(k),
    )
  )
    throw Error("Unsupported personal setting.");
  if (
    p.profile &&
    Object.keys(p.profile).some((k) => !["display_name", "photo"].includes(k))
  )
    throw Error("Unsupported profile value.");
  const profile = parseUserProfile({ ...s.profile, ...p.profile }),
    preferences = parseUserPreferences({ ...s.preferences, ...p.preferences });
  const appearance = p.appearance ?? s.appearance;
  if (
    !profile ||
    !preferences ||
    !["system", "light", "dark"].includes(appearance)
  )
    throw Error("Unsupported personal setting.");
  return { ...s, profile, preferences, appearance };
}
/** One verified User owns this controller for its entire lifetime. No Book or local cache is involved. */
export function createAccountSettingsStore(
  id: string,
  api: AccountSettingsAPI,
  onSignedOut = () => {},
  delay = 250,
) {
  if (!/^[0-9a-f]{64}$/.test(id))
    throw Error("Verified User identity required.");
  let server: AccountSettings = {
    user_id: id,
    revision: "0",
    profile: EMPTY_USER_PROFILE,
    preferences: DEFAULT_USER_PREFERENCES,
    appearance: "system",
  };
  let snapshot: AccountSnapshot = {
    value: server,
    status: "loading",
    error: "",
    loaded: false,
    acknowledged: 0,
  };
  let pending: AccountPatch = {},
    flight: AccountPatch = {},
    busy = false,
    disposed = false,
    epoch = 0,
    acknowledged = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const requests = new Set<AbortController>(),
    listeners = new Set<() => void>();
  const dirty = () => Object.keys(pending).length > 0;
  const publish = (
    status: AccountSnapshot["status"],
    error = "",
    loaded = snapshot.loaded,
  ) => {
    if (disposed) return;
    snapshot = {
      value: apply(apply(server, flight), pending),
      status,
      error,
      loaded,
      acknowledged,
    };
    for (const f of listeners) f();
  };
  function failure(error: unknown) {
    const denied =
      error instanceof APIError && [401, 403].includes(error.status);
    publish(
      denied
        ? "signed-out"
        : error instanceof APIError && error.status === 409
          ? "conflict"
          : "error",
      error instanceof Error
        ? error.message
        : "Personal settings could not be saved.",
    );
    if (denied) {
      pending = {};
      flight = {};
      server = {
        ...server,
        profile: EMPTY_USER_PROFILE,
        preferences: DEFAULT_USER_PREFERENCES,
        appearance: "system",
      };
      publish(
        "signed-out",
        "Sign in again to load your personal settings.",
        false,
      );
      onSignedOut();
    }
  }
  async function load() {
    if (disposed || busy || dirty()) return;
    const current = ++epoch,
      request = new AbortController();
    requests.add(request);
    try {
      const value = parseAccountSettings(await api.load(request.signal), id);
      if (disposed || current !== epoch) return;
      server = value;
      publish("saved", "", true);
    } catch (e) {
      if (!disposed && current === epoch) failure(e);
    } finally {
      requests.delete(request);
    }
  }
  async function flush() {
    if (
      disposed ||
      busy ||
      !dirty() ||
      !snapshot.loaded ||
      snapshot.status === "conflict" ||
      snapshot.status === "signed-out"
    )
      return;
    busy = true;
    epoch++;
    flight = pending;
    pending = {};
    publish("saving");
    const request = new AbortController();
    requests.add(request);
    try {
      const value = parseAccountSettings(
        await api.save(
          { ...flight, expected_revision: server.revision },
          request.signal,
        ),
        id,
      );
      if (disposed) return;
      server = value;
      flight = {};
      acknowledged++;
      publish(dirty() ? "saving" : "saved");
    } catch (e) {
      if (!disposed) {
        pending = merge(flight, pending);
        flight = {};
        failure(e);
      }
    } finally {
      busy = false;
      requests.delete(request);
      if (!disposed && dirty() && snapshot.status === "saving") void flush();
    }
  }
  function reloadSaved() {
    if (disposed || busy) return;
    clearTimeout(timer);
    pending = {}; flight = {}; epoch++;
    publish("loading", "", false);
    return load();
  }
  return {
    reloadSaved,
    getSnapshot: () => snapshot,
    subscribe(fn: () => void) {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    load,
    start() {
      disposed = false;
      return load();
    },
    update(p: AccountPatch) {
      if (disposed || !snapshot.loaded || snapshot.status === "signed-out")
        return;
      apply(snapshot.value, p);
      pending = merge(pending, p);
      epoch++;
      clearTimeout(timer);
      const blocked =
        snapshot.status === "conflict" || snapshot.status === "error";
      publish(
        blocked ? snapshot.status : "saving",
        blocked ? snapshot.error : "",
      );
      if (!blocked) timer = setTimeout(() => void flush(), delay);
    },
    retry() {
      if (snapshot.status === "conflict") {
        return reloadSaved();
      }
      return dirty() ? flush() : load();
    },
    refresh() {
      return load();
    },
    dispose() {
      disposed = true;
      epoch++;
      clearTimeout(timer);
      for (const request of requests) request.abort();
      listeners.clear();
    },
  };
}
