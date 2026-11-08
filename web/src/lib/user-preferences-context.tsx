import { setDisplayPreferences } from "./display-preferences";
import {
  createContext,
  Fragment,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import {
  createPreferenceStore,
  preferenceStorageKey,
  type PreferenceScope,
  type PreferenceStorage,
  type PreferenceStore,
  type PreferenceSnapshot,
  type PreferencePatch,
  type UserPreferences,
  DEFAULT_USER_PREFERENCES,
} from "./user-preferences";

const PreferencesContext = createContext<PreferenceStore | null>(null);
type AccountPresentation = {
  value: Readonly<UserPreferences>;
  persistence: PreferenceSnapshot["persistence"];
  scope: PreferenceScope;
  update: (patch: PreferencePatch) => void;
  editable: boolean;
  statusText: string;
};
const AccountPresentationContext = createContext<AccountPresentation | null>(
  null,
);
export function AccountPreferencesProvider({
  value,
  children,
}: {
  value: AccountPresentation | null;
  children: ReactNode;
}) {
  return (
    <AccountPresentationContext.Provider value={value}>
      <DisplayPreferencesBridge />
      {children}
    </AccountPresentationContext.Provider>
  );
}
const emptyPreferenceSnapshot: PreferenceSnapshot = {
  value: DEFAULT_USER_PREFERENCES,
  persistence: "unavailable",
};
const noPreferenceSubscription = () => () => {};
const getEmptyPreferenceSnapshot = () => emptyPreferenceSnapshot;

function browserStorage() {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** Host identity is explicit. Switching scope constructs the new snapshot
 * synchronously, so the previous user's choices are never rendered for it. */
export function UserPreferencesProvider({
  scope,
  storage,
  children,
}: {
  scope: PreferenceScope;
  storage?: PreferenceStorage | null;
  children: ReactNode;
}) {
  const key = preferenceStorageKey(scope);
  const persistence = storage === undefined ? browserStorage() : storage;
  const store = useMemo(
    () => createPreferenceStore(scope, persistence),
    [key, persistence],
  );
  useEffect(() => {
    if (typeof window === "undefined") return;
    const changed = (event: StorageEvent) => {
      if (
        event.storageArea === persistence &&
        (event.key === key || event.key === null)
      )
        store.reload();
    };
    window.addEventListener("storage", changed);
    return () => window.removeEventListener("storage", changed);
  }, [key, persistence, store]);
  return (
    <PreferencesContext.Provider value={store}>
      <DisplayPreferencesBridge />
      {children}
    </PreferencesContext.Provider>
  );
}

/** Null means no explicit display owner is available; callers must not persist. */
export function useOptionalPreferenceScope(): PreferenceScope | null {
  const account = useContext(AccountPresentationContext);
  const store = useContext(PreferencesContext);
  return account?.scope ?? store?.owner ?? null;
}
export function useUserPreferences() {
  const account = useContext(AccountPresentationContext);
  const store = useContext(PreferencesContext);
  const snapshot = useSyncExternalStore(
    store?.subscribe ?? noPreferenceSubscription,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
  );
  if (account) return { ...account, location: "account" as const };
  if (!store)
    throw new Error("User preferences require an explicit owner scope.");
  return {
    ...snapshot,
    scope: store.owner,
    update: store.update,
    editable: true,
    statusText: undefined,
    location: "browser" as const,
  };
}

const displayKey = (p: UserPreferences) =>
  [p.regional_format, p.date_format, p.negative_style, p.digit_grouping, p.time_format].join("|");
/** Amounts and dates are formatted while pages render. When a format changes, the
 * page content mounts again so every page follows it, not only the ones that read
 * preferences. Wrap page content only: the shell and an open settings dialog stay. */
export function FollowDisplayPreferences({ children }: { children: ReactNode }) {
  const account = useContext(AccountPresentationContext);
  const store = useContext(PreferencesContext);
  const snapshot = useSyncExternalStore(
    store?.subscribe ?? noPreferenceSubscription,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
  );
  const value = account?.value ?? (store ? snapshot.value : undefined);
  return <Fragment key={value ? displayKey(value) : ""}>{children}</Fragment>;
}

/** Renders nothing. As the first child of a preference provider it renders before the
 * pages beside it, so formatters called without preferences use the viewer's. */
function DisplayPreferencesBridge() {
  const account = useContext(AccountPresentationContext);
  const store = useContext(PreferencesContext);
  const snapshot = useSyncExternalStore(
    store?.subscribe ?? noPreferenceSubscription,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
    store?.getSnapshot ?? getEmptyPreferenceSnapshot,
  );
  setDisplayPreferences(account ? account.value : store ? snapshot.value : undefined);
  return null;
}
