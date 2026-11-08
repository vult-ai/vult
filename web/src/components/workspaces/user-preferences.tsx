import { useId } from "react";
import { money } from "../../lib/api";
import { formatDateTime } from "../../lib/date-format";
import {
  REGIONAL_FORMATS,
  type UserPreferences,
} from "../../lib/user-preferences";
import { useUserPreferences } from "../../lib/user-preferences-context";
import { RecordDate } from "../ui/record-date";
import { Select } from "../ui/select";
import type { ReactNode } from "react";

/** The format rows with a line saying where choices are kept.
 * This component has no API client, Book mutation or accounting-policy controls. */
export function UserPreferencesSettings() {
  const { persistence, scope, statusText } = useUserPreferences();
  const persistenceLabel =
    statusText ??
    (persistence === "saved"
      ? scope.kind === "user"
        ? "Saved for you."
        : "Saved in this browser."
      : persistence === "invalid"
        ? "Saved preferences could not be read. Changes apply here without replacing them."
        : "Preferences apply here but could not be saved in this browser.");
  return (
    <>
      <UserPreferenceRows />
      <p role="status" aria-live="polite" className="vult-type-metadata personal-settings-note">
        {persistenceLabel}
      </p>
    </>
  );
}

/** The format rows of the personal settings Preferences tab, beside Appearance.
 * The settings dialog reports saving itself. */
export function UserPreferenceRows() {
  const { value, update, editable } = useUserPreferences();
  const id = useId();
  const sample = { currency: "USD", scale: 2 };
  return (
    <>
      <PreferenceRow label="Language">
        <span className="vult-type-ui">English</span>
      </PreferenceRow>
      <PreferenceRow label="Regional format" htmlFor={`${id}-region`}>
        <Select
          id={`${id}-region`}
          size="sm"
          disabled={!editable}
          aria-label="Regional format"
          value={value.regional_format}
          options={REGIONAL_FORMATS}
          onChange={(event) =>
            update({
              regional_format: event.currentTarget
                .value as UserPreferences["regional_format"],
            })
          }
        />
      </PreferenceRow>
      <PreferenceRow
        label="Date format"
        htmlFor={`${id}-date`}
        hint={
          <span id={`${id}-date-example`}>
            <RecordDate value="2026-10-27" format="full" preferences={value} />
          </span>
        }
      >
        <Select
          id={`${id}-date`}
          size="sm"
          disabled={!editable}
          aria-label="Date format"
          aria-describedby={`${id}-date-example`}
          value={value.date_format}
          options={[
            { value: "standard", label: "Vult default" },
            { value: "month-day-year", label: "MM/DD/YYYY" },
            { value: "day-month-year", label: "DD/MM/YYYY" },
            { value: "iso", label: "YYYY-MM-DD" },
          ]}
          onChange={(event) =>
            update({
              date_format: event.currentTarget
                .value as UserPreferences["date_format"],
            })
          }
        />
      </PreferenceRow>
      <PreferenceRow
        label="Time format"
        htmlFor={`${id}-time`}
        hint={
          <span id={`${id}-time-example`}>
            {formatDateTime("2026-10-27T20:30:00Z", "full", new Date(), value)}
          </span>
        }
      >
        <Select
          id={`${id}-time`}
          size="sm"
          disabled={!editable}
          aria-label="Time format"
          aria-describedby={`${id}-time-example`}
          value={value.time_format}
          options={[
            { value: "24h", label: "24-hour" },
            { value: "12h", label: "12-hour" },
          ]}
          onChange={(event) =>
            update({
              time_format: event.currentTarget
                .value as UserPreferences["time_format"],
            })
          }
        />
      </PreferenceRow>
      <PreferenceRow
        label="Digit grouping"
        htmlFor={`${id}-grouping`}
        hint={
          <span id={`${id}-grouping-example`}>
            {money("123456789", sample, value)}
          </span>
        }
      >
        <Select
          id={`${id}-grouping`}
          size="sm"
          disabled={!editable}
          aria-label="Digit grouping"
          aria-describedby={`${id}-grouping-example`}
          value={value.digit_grouping ? "grouped" : "ungrouped"}
          options={[
            { value: "grouped", label: "Group digits" },
            { value: "ungrouped", label: "No grouping" },
          ]}
          onChange={(event) =>
            update({ digit_grouping: event.currentTarget.value === "grouped" })
          }
        />
      </PreferenceRow>
      <PreferenceRow
        label="Negative numbers"
        htmlFor={`${id}-negative`}
        hint={
          <span id={`${id}-negative-example`}>
            {money("-123456", sample, value)}
          </span>
        }
      >
        <Select
          id={`${id}-negative`}
          size="sm"
          disabled={!editable}
          aria-label="Negative numbers"
          aria-describedby={`${id}-negative-example`}
          value={value.negative_style}
          options={[
            { value: "minus", label: "Minus sign" },
            { value: "parentheses", label: "Parentheses" },
          ]}
          onChange={(event) =>
            update({
              negative_style: event.currentTarget
                .value as UserPreferences["negative_style"],
            })
          }
        />
      </PreferenceRow>
    </>
  );
}

/** One row in the style of the dialog's Appearance row: label and example, then the control. */
function PreferenceRow({ label, htmlFor, hint, children }: { label: string; htmlFor?: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="personal-settings-preference">
      <div>
        {htmlFor ? <label className="vult-type-label" htmlFor={htmlFor}>{label}</label> : <span className="vult-type-label">{label}</span>}
        {hint && <p className="vult-type-metadata">{hint}</p>}
      </div>
      {children}
    </div>
  );
}
