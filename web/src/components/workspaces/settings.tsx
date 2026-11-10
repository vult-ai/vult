import { TextAa, BookOpen01 } from "../../design-system/icons";
import { RecordDate } from "../ui/record-date";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { AccessSettings } from "../access-settings";
import type { Book } from "../../lib/api";
import {
  checkSettings,
  forgetSettings,
  retainedSettingsRequest,
  sendSettings,
  settingsRequest,
  type SettingsOutcome,
  type SettingsRequest,
} from "../../lib/book-settings-write";
import { NativeFieldControl } from "../ui/native-field-control";
import { ErrorMessage } from "../form";
import { ErrorNotice } from "../ui/error-notice";
import { LegalIdentitySettings } from "../book/legal-identity-settings";
import { BookAddressSettings } from "../book/book-address-settings";
import { Button } from "../ui/button";
import { SettingsSection, SettingsRow } from "../ui/settings-section";
import { SettingsLayout } from "../ui/settings-layout";
import { formatAnnualDate } from "../../lib/date-format";
import { BookDataSettings } from "../book/book-data-settings";
import { BookPeriodSettings } from "../book/book-period-settings";
import { BookPayeeSettings } from "../book/book-payee-settings";
import { BookProfileSetting, BookTimezoneSetting } from "../book/book-setup-settings";
import { BookLogoSettings } from "../book/book-logo-settings";

const currencyNames = new Intl.DisplayNames(["en"], { type: "currency" });

export function Settings({
  displayBook,
  book,
  onSaved,
  refreshKey = 0,
  visible = true,
}: {
  displayBook: Book;
  book: Book;
  onSaved: () => void;
  refreshKey?: number;
  visible?: boolean;
}) {
  const [accountAvailable, setAccountAvailable] = useState(false);
  const formId = useId();
  const nameId = `${formId}-name`;
  const active = useRef(true);
  const inFlight = useRef(false);
  const [busy, setBusy] = useState(false);
  const [namesChanged, setNamesChanged] = useState(false);
  const [error, setError] = useState<unknown>();
  // A change whose answer was lost stays frozen until it is confirmed,
  // sent again unchanged, or discarded. It is never edited and resent.
  const [unresolved, setUnresolved] = useState<{
    request: SettingsRequest;
    error: unknown;
  } | null>(() => {
    const request = retainedSettingsRequest(book.id);
    return request ? { request, error: undefined } : null;
  });
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const saved = () => {
    if (active.current) onSaved();
  };
  function settle(outcome: SettingsOutcome) {
    if (!active.current) return;
    if (outcome.status === "recorded") {
      setUnresolved(null);
      setNamesChanged(false);
      saved();
    } else if (outcome.status === "unresolved") {
      setUnresolved({ request: outcome.request, error: outcome.error });
    } else {
      setUnresolved(null);
      setError(outcome.error);
    }
  }
  async function run(step: () => Promise<SettingsOutcome>) {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    try {
      settle(await step());
    } finally {
      inFlight.current = false;
      if (active.current) setBusy(false);
    }
  }
  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (unresolved) return;
    const fields = new FormData(event.currentTarget);
    // The display name is no longer edited here; send the recorded value unchanged.
    const request = settingsRequest(
      book,
      String(fields.get("name") ?? ""),
      book.founder ?? "",
    );
    void run(() => sendSettings(request));
  }
  const frozen = unresolved?.request;
  return (
    <SettingsLayout
      active={visible}
      tabs={[
        {
          id: "book",
          label: "General",
          content: (
            <>
              <BookLogoSettings book={book} onSaved={onSaved} />
              <SettingsSection
                title="Name" icon={TextAa}
                actions={
                  <Button
                    type="submit"
                    form={formId}
                    size="sm"
                    busy={busy}
                    disabled={busy || !!frozen || !namesChanged}
                  >
                    Save
                  </Button>
                }
              >
                <form
                  key={frozen?.idempotency_key ?? "editable"}
                  id={formId}
                  onSubmit={save}
                  onChange={(event) => {
                    const values = new FormData(event.currentTarget);
                    setNamesChanged(values.get("name") !== book.name);
                  }}
                  className="book-settings-details-form"
                >
                  <SettingsRow label="Book name" htmlFor={nameId}>
                    <NativeFieldControl>
                      <input
                        id={nameId}
                        aria-label="Book name"
                        name="name"
                        required
                        maxLength={120}
                        defaultValue={frozen?.name ?? book.name}
                        disabled={busy || !!frozen}
                      />
                    </NativeFieldControl>
                  </SettingsRow>
                  {frozen && (
                    <div className="book-settings-unresolved">
                      <ErrorNotice
                        error={
                          unresolved.error ??
                          new Error(
                            "We couldn’t confirm whether these names were saved.",
                          )
                        }
                        intent="write"
                        resource="settings"
                        pending={busy}
                        onReview={() => run(() => checkSettings(frozen))}
                      />
                      <div className="book-settings-unresolved-actions">
                        <Button
                          size="sm"
                          color="secondary"
                          disabled={busy}
                          onClick={() => void run(() => sendSettings(frozen))}
                        >
                          Send again
                        </Button>
                        <Button
                          size="sm"
                          color="secondary"
                          disabled={busy}
                          onClick={() => {
                            forgetSettings(frozen);
                            setUnresolved(null);
                            // Show the Book as it is now, applied or not.
                            saved();
                          }}
                        >
                          Discard change
                        </Button>
                      </div>
                    </div>
                  )}
                  <ErrorMessage
                    message={error}
                    pending={busy}
                    intent="write"
                    retained="form"
                  />
                </form>
              </SettingsSection>
              <BookAddressSettings key={book.id} book={book} />
            </>
          ),
        },
        {
          id: "accounting",
          label: "Accounting",
          content: (tabActive) => (
            <>
              <SettingsSection
                title="Book setup" icon={BookOpen01}
                description="Set once."
              >
                <SettingsRow label="Book type">
                  {displayBook.profile === "personal" || displayBook.profile === "business" ? (
                    <span>{displayBook.profile === "personal" ? "Personal" : "Business"}</span>
                  ) : (
                    <BookProfileSetting book={book} onSaved={saved} />
                  )}
                </SettingsRow>
                <SettingsRow label="Accounting currency">
                  <span>
                    {currencyNames.of(displayBook.currency)} (
                    {displayBook.currency})
                  </span>
                </SettingsRow>
                <SettingsRow label="Fiscal year starts">
                  <span>
                    {displayBook.fiscal_year_start_month &&
                    displayBook.fiscal_year_start_day
                      ? formatAnnualDate(
                          displayBook.fiscal_year_start_month,
                          displayBook.fiscal_year_start_day,
                        )
                      : "Not recorded"}
                  </span>
                </SettingsRow>
                <SettingsRow label="Timezone">
                  {displayBook.accounting_timezone ? (
                    <span>{displayBook.accounting_timezone}</span>
                  ) : (
                    <BookTimezoneSetting book={book} onSaved={saved} />
                  )}
                </SettingsRow>
              </SettingsSection>
              <BookPeriodSettings key={book.id} book={book} />
              <BookPayeeSettings key={`payees:${book.id}`} book={book} />
            </>
          ),
        },
        {
          id: "identity",
          label: "Legal identity",
          content: (tabActive) => (
            <LegalIdentitySettings
              key={book.id}
              book={book}
              onSaved={saved}
              presentation="settings"
              refreshKey={refreshKey}
              active={tabActive}
            />
          ),
        },
        {
          id: "data",
          label: "Data",
          content: <BookDataSettings book={book} />,
        },
        {
          id: "account",
          // The agent connections that reach this Book, as `vult connection` lists them.
          label: "Connections",
          visible: accountAvailable,
          eager: true,
          content: (tabActive) => (
            <AccessSettings
              book={book}
              onAvailabilityChange={setAccountAvailable}
              active={tabActive}
              probe={visible}
            />
          ),
        },
      ]}
    />
  );
}
