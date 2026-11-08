import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  Monitor01,
  Moon01,
  Settings01,
  Sun,
  HelpCircle,
} from "../../design-system/icons";
import { Button as AriaButton, SubmenuTrigger } from "react-aria-components";
import { Avatar } from "../../design-system/catalog/components/vult-ui/avatar";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { useBreakpoint } from "../../design-system/catalog/hooks/use-breakpoint";
import { normalizeThemePreference } from "../../design-system/theme-preference";
import { useTheme } from "../../design-system/theme";
import { documentationURL } from "../../lib/documentation";
import {
  createProfileStore,
  profileStorageKey,
  profileInitials,
  readProfilePhoto,
} from "../../lib/user-profile";
import type {
  PreferenceScope,
  PreferenceStorage,
} from "../../lib/user-preferences";
import { PersonalSettings } from "./personal-settings";
import { usePersonalAccount } from "./personal-settings-host";

const browser: PreferenceScope = { kind: "browser" };
const appearances = [
  { id: "system", label: "System", icon: Monitor01 },
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon01 },
] as const;
function localStorageIfAvailable() {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** Personal identity is independent of the selected Book and its owner labels. */
export function ProfileMenu({
  scope = browser,
  storage,
  decodePhoto = readProfilePhoto,
}: {
  scope?: PreferenceScope;
  storage?: PreferenceStorage | null;
  decodePhoto?: (file: File) => Promise<string>;
}) {
  const account = usePersonalAccount();
  scope = account?.scope ?? scope;
  const persistence =
    storage === undefined ? localStorageIfAvailable() : storage;
  const key = profileStorageKey(scope);
  const store = useMemo(
    () => createProfileStore(scope, persistence),
    [key, persistence],
  );
  const snapshot = useSyncExternalStore(
    store.subscribe,
    store.getSnapshot,
    store.getSnapshot,
  );
  const [open, setOpen] = useState(false);
  const [upload, setUpload] = useState({ key, pending: false, error: "" });
  const generation = useRef(0);
  const currentKey = useRef(key);
  if (currentKey.current !== key) {
    currentKey.current = key;
    generation.current++;
  }
  const trigger = useRef<HTMLButtonElement>(null);
  const hasSubmenuSpace = useBreakpoint("sm");
  const theme = useTheme();
  const appearance = appearances.find(
    (option) => option.id === theme.preference,
  )!;
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  useEffect(() => {
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
  const changeOpen = (next: boolean) => {
    if (!next) {
      generation.current++;
      setUpload({ key, pending: false, error: "" });
    }
    if (next) account?.refresh();
    setOpen(next);
  };
  async function photo(file: File) {
    const request = ++generation.current;
    setUpload({ key, pending: true, error: "" });
    try {
      const value = await decodePhoto(file);
      if (request !== generation.current || key !== currentKey.current) return;
      if (account) account.update({ profile: { photo: value } });
      else store.update({ photo: value });
      setUpload({ key, pending: false, error: "" });
    } catch (error) {
      if (request === generation.current && key === currentKey.current)
        setUpload({
          key,
          pending: false,
          error:
            error instanceof Error
              ? error.message
              : "This photo could not be read.",
        });
    }
  }
  const value = account?.profile ?? snapshot.value;
  return (
    <>
      <Dropdown.Root>
        <AriaButton
          ref={trigger}
          aria-label="Open profile menu"
          className="vult-action-target vult-action-fill outline-focus-ring flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-full"
        >
          <Avatar
            key={value.photo || "empty"}
            border
            size="xs"
            src={value.photo}
            initials={profileInitials(value.display_name)}
            alt=""
          />
        </AriaButton>
        <Dropdown.Popover
          placement="bottom right"
          offset={12}
          className="motion-reduce:animate-none [[data-vult-focus-visible=true]_&]:animate-none"
        >
          <div className="border-b border-secondary px-2.5 py-2">
            <p className="vult-type-label truncate font-medium text-primary">
              {value.display_name.trim() || "Your profile"}
            </p>
            <p className="vult-type-metadata truncate text-quaternary">
              Personal settings
            </p>
          </div>
          <Dropdown.Menu aria-label="Profile" selectionMode="none">
            <Dropdown.Item
              id="profile"
              icon={Settings01}
              selectionIndicator="none"
              onAction={() => changeOpen(true)}
            >
              Profile & preferences
            </Dropdown.Item>
            <Dropdown.Item
              id="help"
              icon={HelpCircle}
              selectionIndicator="none"
              href={documentationURL()}
              target="_blank"
              rel="noopener noreferrer"
            >
              Help & documentation
            </Dropdown.Item>
            <SubmenuTrigger>
              <Dropdown.Item
                id="appearance"
                isDisabled={!!account && !account.editable}
                icon={appearance.icon}
                addon={appearance.label}
              >
                Appearance
              </Dropdown.Item>
              <Dropdown.Popover
                placement={hasSubmenuSpace ? "right bottom" : "bottom right"}
                offset={hasSubmenuSpace ? -4 : 4}
                className="motion-reduce:animate-none [[data-vult-focus-visible=true]_&]:animate-none"
              >
                <Dropdown.Menu
                  aria-label="Appearance"
                  selectionMode="single"
                  disallowEmptySelection
                  selectedKeys={[theme.preference]}
                  onSelectionChange={(keys) => {
                    if (keys === "all" || (account && !account.editable))
                      return;
                    const next = normalizeThemePreference(
                      String(Array.from(keys)[0]),
                    );
                    if (next) theme.setPreference(next);
                  }}
                >
                  {appearances.map((option) => (
                    <Dropdown.Item
                      key={option.id}
                      id={option.id}
                      icon={option.icon}
                    >
                      {option.label}
                    </Dropdown.Item>
                  ))}
                </Dropdown.Menu>
              </Dropdown.Popover>
            </SubmenuTrigger>
          </Dropdown.Menu>
        </Dropdown.Popover>
      </Dropdown.Root>
      <PersonalSettings
        open={open}
        onOpenChange={changeOpen}
        returnTo={trigger.current}
        profile={value}
        persistence={snapshot.persistence}
        onName={(display_name) =>
          account
            ? account.update({ profile: { display_name } })
            : store.update({ display_name })
        }
        onPhoto={photo}
        onRemovePhoto={() => {
          generation.current++;
          setUpload({ key, pending: false, error: "" });
          if (account) account.update({ profile: { photo: null } });
          else store.update({ photo: null });
        }}
        photoBusy={upload.key === key && upload.pending}
        photoError={upload.key === key ? upload.error : ""}
        editable={account?.editable ?? true}
        statusText={account?.status}
        saveError={account?.error}
        onRetry={account?.retry}
        browserCopy={account?.legacy}
      />
    </>
  );
}
