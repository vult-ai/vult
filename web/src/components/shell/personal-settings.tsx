import { ErrorMessage } from "../form";
import { FieldError } from "../ui/error-notice";
import { useId, useRef, useState } from "react";
import { Tabs, TabPanel } from "react-aria-components";
import { Avatar } from "../../design-system/catalog/components/vult-ui/avatar";
import { useTheme, type ThemePreference } from "../../design-system/theme";
import { profileInitials, type UserProfile } from "../../lib/user-profile";
import {
  REGIONAL_FORMATS,
  type PreferencePersistence,
} from "../../lib/user-preferences";
import type { LegacyPersonalData } from "../../lib/legacy-personal-data";
import { Dialog } from "../ui/dialog";
import { TabStrip } from "../ui/tab-strip";
import { Button } from "../ui/button";
import { NativeFieldControl } from "../ui/native-field-control";
import { Select } from "../ui/select";
import { UserPreferenceRows } from "../workspaces/user-preferences";
import { useOptionalPreferenceScope } from "../../lib/user-preferences-context";
import "./personal-settings.css";

export function PersonalSettings({
  open,
  onOpenChange,
  returnTo,
  profile,
  persistence,
  onName,
  onPhoto,
  onRemovePhoto,
  photoBusy,
  photoError,
  editable = true,
  statusText,
  saveError,
  onRetry,
  browserCopy,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  returnTo: HTMLElement | null;
  profile: Readonly<UserProfile>;
  persistence: PreferencePersistence;
  onName: (name: string) => void;
  onPhoto: (file: File) => void;
  onRemovePhoto: () => void;
  photoBusy: boolean;
  photoError: string;
  editable?: boolean;
  statusText?: string;
  saveError?: string;
  onRetry?: () => void;
  browserCopy?: {
    owner: string;
    copy: LegacyPersonalData;
    busy: boolean;
    canImport: boolean;
    import: () => void;
    keep: () => void;
  };
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const theme = useTheme();
  // Formats need an owner to be kept for; without one the rows are not offered.
  const formatsOwner = useOptionalPreferenceScope();
  const [reviewed, setReviewed] = useState("");
  const copyKey = browserCopy
    ? `${browserCopy.owner}:${browserCopy.copy.fingerprint}`
    : "";
  const reviewing = !!copyKey && reviewed === copyKey;
  const saveStatus =
    statusText ??
    (persistence === "saved"
      ? ""
      : persistence === "invalid"
        ? "Saved profile could not be read. Changes apply here without replacing it."
        : "Changes apply here but could not be saved in this browser.");
  return (
    <Dialog
      title="Your settings"
      description={null}
      open={open}
      onOpenChange={onOpenChange}
      returnTo={returnTo}
      size="small"
      className="personal-settings-dialog"
    >
      <Tabs defaultSelectedKey="profile" className="personal-settings-tabs">
        <TabStrip
          aria-label="Personal settings"
          items={[
            { id: "profile", label: "Profile" },
            { id: "preferences", label: "Preferences" },
          ]}
        />
        <TabPanel
          id="profile"
          shouldForceMount
          className="personal-settings-panel"
        >
          <div className="personal-settings-photo">
            <Avatar
              key={profile.photo || "empty"}
              size="2xl"
              src={profile.photo}
              initials={profileInitials(profile.display_name)}
              alt="Profile photo"
            />
            <div className="personal-settings-photo-controls">
              <div className="personal-settings-photo-buttons">
                <Button
                  color="secondary"
                  size="sm"
                  busy={photoBusy}
                  disabled={!editable}
                  onClick={() => input.current?.click()}
                >
                  Upload photo
                </Button>
                {profile.photo && (
                  <Button
                    color="tertiary"
                    size="sm"
                    disabled={!editable}
                    onClick={onRemovePhoto}
                  >
                    Remove
                  </Button>
                )}
              </div>
              <p className="vult-type-metadata">JPG or PNG. Up to 2 MB.</p>
            </div>
            <input
              ref={input}
              hidden
              tabIndex={-1}
              type="file"
              accept="image/jpeg,image/png"
              aria-label="Upload profile photo"
              aria-invalid={!!photoError}
              aria-describedby={photoError ? `${id}-photo-error` : undefined}
              onChange={(event) => {
                const file = event.currentTarget.files?.[0];
                event.currentTarget.value = "";
                if (file) onPhoto(file);
              }}
            />
          </div>
          <FieldError id={`${id}-photo-error`} error={photoError} />
          <div className="personal-settings-field">
            <label className="vult-type-label" htmlFor={`${id}-name`}>
              Display name
            </label>
            <NativeFieldControl>
              <input
                id={`${id}-name`}
                aria-label="Display name"
                disabled={!editable}
                name="display_name"
                autoComplete="nickname"
                value={profile.display_name}
                maxLength={80}
                placeholder="Your name"
                onChange={(event) => onName(event.currentTarget.value)}
              />
            </NativeFieldControl>
          </div>
          {browserCopy && (
            <section
              className="personal-settings-legacy"
              aria-label="Browser settings copy"
            >
              <p className="vult-type-label">Browser settings found</p>
              <p className="vult-type-metadata">
                Review this browser’s copy before saving it to your profile.
              </p>
              {!reviewing ? (
                <Button
                  color="secondary"
                  size="sm"
                  disabled={!editable}
                  onClick={() => setReviewed(copyKey)}
                >
                  Review browser copy
                </Button>
              ) : (
                <>
                  {browserCopy.copy.profile && (
                    <div className="personal-settings-legacy-preview">
                      <Avatar
                        size="md"
                        src={browserCopy.copy.profile.photo}
                        initials={profileInitials(
                          browserCopy.copy.profile.display_name,
                        )}
                        alt="Browser profile photo"
                      />
                      <div>
                        <p className="vult-type-metadata">
                          Current name:{" "}
                          {profile.display_name || "No display name"}
                        </p>
                        <p className="vult-type-metadata">
                          Browser name:{" "}
                          {browserCopy.copy.profile.display_name ||
                            "No display name"}
                        </p>
                      </div>
                    </div>
                  )}
                  {browserCopy.copy.patch.appearance && (
                    <p className="vult-type-metadata">
                      Browser appearance:{" "}
                      {browserCopy.copy.patch.appearance === "system"
                        ? "System"
                        : browserCopy.copy.patch.appearance === "dark"
                          ? "Dark"
                          : "Light"}
                    </p>
                  )}
                  {browserCopy.copy.patch.preferences && (
                    <p className="vult-type-metadata">
                      Browser regional format:{" "}
                      {
                        REGIONAL_FORMATS.find(
                          (option) =>
                            option.value ===
                            browserCopy.copy.patch.preferences?.regional_format,
                        )?.label
                      }
                      . Includes date, time and number preferences.
                    </p>
                  )}
                  {browserCopy.copy.warning && (
                    <p className="vult-type-metadata">
                      Some browser values could not be read. The original copy
                      has been kept.
                    </p>
                  )}
                  <div className="personal-settings-photo-buttons">
                    <Button
                      size="sm"
                      disabled={!browserCopy.canImport || photoBusy}
                      busy={browserCopy.busy}
                      onClick={browserCopy.import}
                    >
                      Use browser copy
                    </Button>
                    <Button
                      color="tertiary"
                      size="sm"
                      disabled={browserCopy.busy || photoBusy}
                      onClick={browserCopy.keep}
                    >
                      Keep saved settings
                    </Button>
                  </div>
                </>
              )}
            </section>
          )}
        </TabPanel>
        <TabPanel
          id="preferences"
          shouldForceMount
          className="personal-settings-panel"
        >
          <div className="personal-settings-preference">
            <div>
              <label className="vult-type-label" htmlFor={`${id}-theme`}>
                Appearance
              </label>
              <p className="vult-type-metadata">
                Follow your system or choose a theme.
              </p>
            </div>
            <Select
              id={`${id}-theme`}
              size="sm"
              aria-label="Theme preference"
              disabled={!editable}
              value={theme.preference}
              onChange={(event) =>
                theme.setPreference(
                  event.currentTarget.value as ThemePreference,
                )
              }
              options={[
                { value: "system", label: "System" },
                { value: "light", label: "Light" },
                { value: "dark", label: "Dark" },
              ]}
            />
          </div>
          {formatsOwner && <UserPreferenceRows />}
        </TabPanel>
      </Tabs>
      <p className="personal-settings-status vult-type-metadata" role="status">
        {saveStatus}
      </p>
      <ErrorMessage message={saveError} intent="write" retained="form" />
      {onRetry && (
        <Button color="secondary" size="sm" onClick={onRetry}>
          {statusText?.includes("Reload") ? "Reload saved settings" : "Retry"}
        </Button>
      )}
    </Dialog>
  );
}
