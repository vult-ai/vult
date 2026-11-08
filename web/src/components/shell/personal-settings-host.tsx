import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  useRef,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { Theme } from "../../design-system/theme";
import { APIError } from "../../lib/api";
import { readSession, type Session } from "../../lib/session";
import {
  AccountPreferencesProvider,
  UserPreferencesProvider,
} from "../../lib/user-preferences-context";
import {
  accountSettingsAPI,
  createAccountSettingsStore,
  type AccountSettings,
  type AccountPatch,
  type AccountSnapshot,
} from "../../lib/account-settings";
import { EMPTY_USER_PROFILE, type UserProfile } from "../../lib/user-profile";
import {
  DEFAULT_USER_PREFERENCES,
  type PreferenceScope,
} from "../../lib/user-preferences";

import {
  readLegacyPersonalData,
  legacyChoiceRemembered,
  rememberLegacyChoice,
  legacyDiffers,
  legacyImportMatches,
  type LegacyPersonalData,
} from "../../lib/legacy-personal-data";

type PersonalAccount = {
  contactSession?: { user_id: string; csrf: string };
  scope: PreferenceScope;
  profile: Readonly<UserProfile>;
  editable: boolean;
  status: string;
  error: string;
  retry?: () => void;
  refresh: () => void;
  update: (patch: AccountPatch) => void;
  preferences?: AccountSettings["preferences"];
  legacy?: {
    owner: string;
    copy: LegacyPersonalData;
    busy: boolean;
    canImport: boolean;
    import: () => void;
    keep: () => void;
  };
};
const Context = createContext<PersonalAccount | null>(null);
export const usePersonalAccount = () => useContext(Context);
const empty: AccountSnapshot = {
  value: {
    user_id: "",
    revision: "0",
    preferences: DEFAULT_USER_PREFERENCES,
    profile: EMPTY_USER_PROFILE,
    appearance: "system",
  },
  status: "loading",
  error: "",
  loaded: false,
};
const noSubscribe = () => () => {};
const emptySnapshot = () => empty;
/** This host is above Book composition. A verified session, never the selected Book, owns its lifetime. */
export function PersonalSettingsHost({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null),
    [sessionError, setSessionError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let live = true;
    let sequence = 0;
    async function identify() {
      const attempt = ++sequence;
      try {
        const next = await readSession();
        if (!live || attempt !== sequence) return;
        if (
          !next.user_id ||
          !/^[0-9a-f]{64}$/.test(next.user_id) ||
          !next.csrf
        )
          throw new APIError(
            next.mode === "local"
              ? "Personal settings are not ready on this instance. Retry when it is ready."
              : "Sign in to load your personal settings.",
            next.mode === "local" ? 503 : 403,
          );
        setSession(next);
        setSessionError("");
      } catch (e) {
        if (live && attempt === sequence) {
          if (e instanceof APIError && [401, 403].includes(e.status))
            setSession(null);
          setSessionError(
            e instanceof Error
              ? e.message
              : "Personal settings are unavailable.",
          );
        }
      }
    }
    void identify();
    window.addEventListener("focus", identify);
    return () => {
      live = false;
      sequence++;
      window.removeEventListener("focus", identify);
    };
  }, [refresh]);
  const key = session?.user_id
    ? `user:${session.mode}:${session.user_id}`
    : "unavailable";
  const activeKey = useRef(key);
  activeKey.current = key;
  const store = useMemo(
    () =>
      session?.user_id
        ? createAccountSettingsStore(
            session.user_id,
            accountSettingsAPI(session.csrf),
            () => {
              if (activeKey.current !== key) return;
              setSession(null);
              setRefresh((v) => v + 1);
            },
          )
        : null,
    [key, session?.csrf],
  );
  const snapshot = useSyncExternalStore(
    store?.subscribe ?? noSubscribe,
    store?.getSnapshot ?? emptySnapshot,
    store?.getSnapshot ?? emptySnapshot,
  );
  useEffect(() => {
    if (!store) return;
    void store.start();
    const refresh = () => {
      void store.refresh();
    };
    window.addEventListener("focus", refresh);
    return () => {
      window.removeEventListener("focus", refresh);
      store.dispose();
    };
  }, [store]);
  const [legacy, setLegacy] = useState<{
    owner: string;
    copy: LegacyPersonalData;
  } | null>(null);
  const [migration, setMigration] = useState<{
    owner: string;
    copy: LegacyPersonalData;
    acknowledged: number;
  } | null>(null);
  const storage = useMemo(() => {
    try {
      return window.localStorage;
    } catch {
      return null;
    }
  }, []);
  useEffect(() => {
    setLegacy(null);
    setMigration(null);
  }, [key, store]);
  useEffect(() => {
    if (!session?.user_id || !snapshot.loaded) return;
    let live = true;
    const user = session.user_id;
    void readLegacyPersonalData(storage)
      .then((copy) => {
        if (
          !live ||
          activeKey.current !== key ||
          !copy ||
          legacyChoiceRemembered(storage, user, copy) ||
          !legacyDiffers(copy, snapshot.value)
        )
          return;
        setLegacy({ owner: key, copy });
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [key, store, snapshot.loaded, storage]);
  useEffect(() => {
    if (
      !migration ||
      migration.owner !== key ||
      !session?.user_id ||
      snapshot.status !== "saved" ||
      (snapshot.acknowledged ?? 0) <= migration.acknowledged ||
      !legacyImportMatches(migration.copy, snapshot.value)
    )
      return;
    rememberLegacyChoice(storage, session.user_id, migration.copy);
    setLegacy(null);
    setMigration(null);
  }, [key, migration, snapshot, storage]);
  const copy = legacy?.owner === key ? legacy.copy : null;
  const legacyBusy = migration?.owner === key && snapshot.status === "saving";
  function importBrowserCopy() {
    if (
      !copy ||
      !store ||
      !session?.user_id ||
      snapshot.status !== "saved" ||
      legacyBusy ||
      !Object.keys(copy.patch).length
    )
      return;
    setMigration({
      owner: key,
      copy,
      acknowledged: snapshot.acknowledged ?? 0,
    });
    store.update(copy.patch);
  }
  const value: PersonalAccount = {
    contactSession:
      session?.personal_data_version === "1" &&
      session.user_id &&
      snapshot.loaded &&
      !sessionError
        ? { user_id: session.user_id, csrf: session.csrf }
        : undefined,
    scope: session?.user_id
      ? { kind: "user", id: session.user_id }
      : { kind: "browser" },
    profile: snapshot.value.profile,
    editable:
      !!store &&
      snapshot.loaded &&
      snapshot.status !== "signed-out" &&
      !legacyBusy,
    status:
      sessionError ||
      (legacyBusy
        ? "Saving…"
        : !store
          ? "Loading your personal settings…"
          : snapshot.status === "saved"
            ? ""
            : snapshot.status === "saving"
              ? "Saving…"
              : snapshot.status === "loading"
                ? "Loading your personal settings…"
                : snapshot.status === "conflict"
                  ? "Changes were not saved. Reload the settings saved on another device."
                  : "Changes have not been saved."),
    error: sessionError || snapshot.error,
    retry: sessionError
      ? () => setRefresh((v) => v + 1)
      : ["error", "conflict"].includes(snapshot.status)
        ? () => {
            void store?.retry();
          }
        : undefined,
    refresh: () => {
      if (!legacyBusy) void store?.refresh();
    },
    update: (patch) => {
      if (!legacyBusy) store?.update(patch);
    },
    preferences: snapshot.value.preferences,
    legacy: copy
      ? {
          owner: key,
          copy,
          busy: legacyBusy,
          canImport:
            snapshot.status === "saved" &&
            !legacyBusy &&
            !!Object.keys(copy.patch).length,
          import: importBrowserCopy,
          keep: () => {
            if (!legacyBusy && session?.user_id) {
              rememberLegacyChoice(storage, session.user_id, copy);
              setLegacy(null);
              setMigration(null);
              void store?.reloadSaved();
            }
          },
        }
      : undefined,
  };
  return (
    <Theme
      initialPreference="system"
      initialTheme="light"
      preferenceOverride={snapshot.value.appearance}
      onPreferenceChange={
        store && snapshot.loaded && !legacyBusy
          ? (v) => store.update({ appearance: v })
          : undefined
      }
      isolateStorage={true}
    >
      <Context.Provider value={value}>
        <UserPreferencesProvider scope={{ kind: "browser" }} storage={null}>
          <AccountPreferencesProvider
            value={{
              value: snapshot.value.preferences,
              persistence:
                snapshot.status === "saved" ? "saved" : "unavailable",
              scope: value!.scope,
              update: (preferences) => store?.update({ preferences }),
              editable: value!.editable,
              statusText: value!.status,
            }}
          >
            {children}
          </AccountPreferencesProvider>
        </UserPreferencesProvider>
      </Context.Provider>
    </Theme>
  );
}
