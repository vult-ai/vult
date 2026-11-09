import { IdentificationCard } from "../../design-system/icons";
import { formatDate, formatDateTime } from "../../lib/date-format";
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { get, bookURL, type Book } from "../../lib/api";
import { settingsPost as post } from "../../lib/settings-requests";
import {
  identityLabel,
  type AccountingSubject,
  type IdentityView,
  type LegalCatalog,
  type LegalIdentityInput,
  type LegalIdentity,
} from "../../lib/legal-identity";
import { Field, ErrorMessage } from "../form";
import { Button } from "../ui/button";
import { useDialogPending } from "../ui/dialog";
import { SettingsDisclosure, SettingsSection, SettingsRow } from "../ui/settings-section";
import { BusinessStructureChoice } from "../onboarding/business-structure-choice";
import { OnboardingChoice } from "../onboarding/onboarding-choice";
import { nationalJurisdiction } from "../onboarding/business-identity";
import { IdentityCountryChoice } from "./identity-country-choice";
import {
  identitySettingsFacts,
  identitySettingsChanges,
} from "./identity-settings-facts";
import { LoadingState } from "../ui/loading-state";
import { Notice } from "../ui/notice";

type Change = {
  book_id: string;
  revision: number;
  kind?: string;
  identity: LegalIdentityInput;
  reason: string;
  effective_date: string;
  idempotency_key: string;
};
const storageKey = (book: string) => `vult:identity-change:${book}`;
function retained(book: string): Change | null {
  try {
    const value: (Omit<Change, "kind"> & { kind?: string | null }) | null = JSON.parse(
      sessionStorage.getItem(storageKey(book)) || "null",
    );
    if (value?.book_id !== book || typeof value.idempotency_key !== "string") return null;
    // Older drafts represented the absent kind as null.
    if (value.kind === null) delete value.kind;
    return value as Change;
  } catch {
    return null;
  }
}

export function LegalIdentitySettings({
  book,
  onSaved,
  presentation,
  refreshKey = 0,
  active = true,
}: {
  book: Book;
  onSaved: () => void;
  presentation?: "settings";
  refreshKey?: number;
  active?: boolean;
}) {
  const [data, setData] = useState<{
    catalog: LegalCatalog;
    view: IdentityView;
  } | null>(null);
  const [editing, setEditing] = useState(
    presentation !== "settings" || !!retained(book.id),
  );
  const [reload, setReload] = useState(0);
  const [error, setError] = useState<unknown>();
  const loadedRead = useRef("");
  const readKey = JSON.stringify([book.id, refreshKey, reload]);
  useEffect(() => {
    if (!active || loadedRead.current === readKey) return;
    let current = true;
    const controller = new AbortController();
    Promise.all([
      get<{ catalog: LegalCatalog }>(
        "legal-identity-catalog",
        controller.signal,
      ),
      get<IdentityView>(bookURL("book-identity", book.id), controller.signal),
    ])
      .then(([catalog, view]) => {
        if (current) {
          setData({ catalog: catalog.catalog, view });
          loadedRead.current = readKey;
          setError(undefined);
        }
      })
      .catch((e) => {
        if (current) setError(e);
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [book.id, refreshKey, active, readKey]);
  const content = (
    <>
      <ErrorMessage message={error} intent="read" resource="settings" />
      {data && presentation === "settings" && !editing ? (
        <IdentitySummary subject={data.view.subject} catalog={data.catalog} />
      ) : data ? (
        <IdentityEditor
          key={`${book.id}:${data.view.subject.revision}`}
          book={book}
          catalog={data.catalog}
          subject={data.view.subject}
          onSaved={() => {
            setEditing(presentation !== "settings");
            setReload((v) => v + 1);
            onSaved();
          }}
          onCancel={() => setEditing(false)}
          grouped={presentation === "settings"}
        />
      ) : (
        !error && <LoadingState scope="inline" label="Loading identity…" />
      )}
      {data &&
        (presentation !== "settings" || data.view.history.length > 0) &&
        (presentation === "settings" ? (
          <SettingsDisclosure
            title="Change history"
            className="settings-identity-history"
            meta={`${data.view.history.length}${data.view.has_more ? "+" : ""}`}
          >
            <IdentityHistoryList
              key={`history:${book.id}:${data.view.subject.revision}`}
              book={book}
              initial={data.view}
            />
          </SettingsDisclosure>
        ) : (
          <IdentityHistoryList
            key={`history:${book.id}:${data.view.subject.revision}`}
            book={book}
            initial={data.view}
          />
        ))}
    </>
  );
  return presentation === "settings" ? (
    <SettingsSection
      title="Legal identity" icon={IdentificationCard}
      description={
        undefined
      }
      actions={
        data && !editing ? (
          <Button color="secondary" size="md" onClick={() => setEditing(true)}>
            {identitySettingsFacts(
              data.view.subject.kind,
              data.view.subject.identity,
              data.catalog,
            ).some((f) => f.value)
              ? "Edit details"
              : "Add details"}
          </Button>
        ) : undefined
      }
    >
      <div className="book-settings-identity">{content}</div>
    </SettingsSection>
  ) : (
    <section aria-label="Legal identity">
      <h3>Legal identity</h3>
      <p className="muted">
        Optional facts about the person or organization this Book represents.
        These choices do not change your entries or establish tax treatment.
      </p>
      {content}
    </section>
  );
}

function IdentityEditor({
  book,
  catalog,
  subject,
  onSaved,
  onCancel,
  grouped = false,
}: {
  book: Book;
  catalog: LegalCatalog;
  subject: AccountingSubject;
  onSaved: () => void;
  onCancel?: () => void;
  grouped?: boolean;
}) {
  const [pending] = useState(() => retained(book.id));
  const saved = pending?.identity || subject.identity;
  const [kind, setKind] = useState(pending?.kind || subject.kind || "");
  const [legalName, setLegalName] = useState(saved.legal_name || "");
  const [country, setCountry] = useState(
    saved.formation_or_governing_country_code || "",
  );
  const [jurisdiction, setJurisdiction] = useState(
    saved.legal_jurisdiction_id ||
      (saved.custom_legal_jurisdiction ? "custom" : ""),
  );
  const [form, setForm] = useState(
    saved.legal_form_id || (saved.custom_legal_form ? "custom" : ""),
  );
  const [customForm, setCustomForm] = useState(saved.custom_legal_form || "");
  const [registrationCountry, setRegistrationCountry] = useState(
    saved.registrations?.[0]?.country_code || "",
  );
  const [residences, setResidences] = useState(
    saved.tax_residence_country_codes || [],
  );
  const [reviewing, setReviewing] = useState(!!pending);
  const [review, setReview] = useState<Change | null>(pending);
  const [dirty, setDirty] = useState(false);
  const [taxTouched, setTaxTouched] = useState(false);
  const [overlayTouched, setOverlayTouched] = useState(false);
  const [overlay, setOverlay] = useState(saved.overlay_ids?.[0] || "");
  const [tax, setTax] = useState(saved.tax_classification_ids?.[0] || "");
  const [busy, setBusy] = useState(false);
  const [frozen, setFrozen] = useState(!!pending);
  const request = useRef<Change | null>(pending);
  const inFlight = useRef(false);
  const [error, setError] = useState<unknown>();
  useDialogPending(busy);
  const knownCountry = catalog.countries.some((c) => c.code === country);
  const jurisdictions = catalog.jurisdictions.filter(
    (j) => j.country_code === country,
  );
  const forms = catalog.forms.filter(
    (f) =>
      f.country_code === country && f.jurisdiction_ids.includes(jurisdiction),
  );
  const overlays = catalog.overlays.filter(
    (o) => o.country_code === country && o.legal_form_ids.includes(form),
  );
  const taxChoices = catalog.tax_classifications.filter(
    (t) => t.country_code === country,
  );
  function changeCountry(value: string) {
    setCountry(value === "custom" ? "XX" : value);
    setJurisdiction(grouped ? nationalJurisdiction(catalog, value) || "" : "");
    setForm("");
    setCustomForm("");
    setOverlay("");
    setTax("");
    setOverlayTouched(true);
    setTaxTouched(true);
    setDirty(true);
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (inFlight.current) return;
    setError("");
    const f = new FormData(event.currentTarget);
    const registration = String(f.get("registration_number") || "").trim();
    const candidate: Change = {
      book_id: book.id,
      revision: subject.revision,
      ...(kind ? { kind } : {}),
      identity: {
        registration_status:
          (String(
            f.get("registration_status") || "",
          ) as LegalIdentityInput["registration_status"]) || undefined,
        legal_name: legalName,
        formation_or_governing_country_code: country,
        legal_jurisdiction_id: jurisdiction === "custom" ? "" : jurisdiction,
        custom_legal_jurisdiction:
          jurisdiction === "custom"
            ? String(f.get("custom_jurisdiction") || "")
            : "",
        legal_form_id: form === "custom" ? "" : form,
        custom_legal_form:
          form === "custom"
            ? grouped
              ? customForm
              : String(f.get("custom_form") || "")
            : "",
        overlay_ids: overlayTouched
          ? overlay
            ? [overlay]
            : []
          : saved.overlay_ids || (overlay ? [overlay] : []),
        tax_classification_ids: taxTouched
          ? tax
            ? [tax]
            : []
          : saved.tax_classification_ids || (tax ? [tax] : []),
        custom_overlays: saved.custom_overlays || [],
        custom_tax_classifications: saved.custom_tax_classifications || [],
        tax_residence_country_codes: grouped
          ? residences
          : String(f.get("tax_residence") || "")
              .split(",")
              .map((c) => c.trim())
              .filter(Boolean),
        registrations: registration
          ? [
              {
                authority: String(f.get("registration_authority") || ""),
                number: registration,
                label: saved.registrations?.[0]?.label,
                country_code: grouped
                  ? registrationCountry
                  : String(f.get("registration_country") || ""),
              },
              ...(saved.registrations?.slice(1) || []),
            ]
          : saved.registrations?.slice(1) || [],
      },
      reason: String(f.get("reason") || ""),
      effective_date: String(f.get("effective_date") || ""),
      idempotency_key: crypto.randomUUID(),
    };
    if (grouped && !reviewing) {
      setReview(candidate);
      setReviewing(true);
      return;
    }
    inFlight.current = true;
    setBusy(true);
    if (!request.current)
      request.current =
        grouped && review
          ? {
              ...review,
              reason: candidate.reason,
              effective_date: candidate.effective_date,
            }
          : candidate;
    try {
      try {
        sessionStorage.setItem(
          storageKey(book.id),
          JSON.stringify(request.current),
        );
      } catch {
        /* Stable in-memory attempt remains. */
      }
      setFrozen(true);
      await post("book-identity", request.current);
      try {
        sessionStorage.removeItem(storageKey(book.id));
      } catch {
        /* The committed key is still idempotent. */
      }
      onSaved();
    } catch (e) {
      setError(e);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }
  return (
    <form
      onSubmit={submit}
      className="settings-identity-editor"
      onChange={() => setDirty(true)}
    >
      <div className="identity-edit-fields" hidden={grouped && reviewing}>
        <fieldset
          disabled={busy || frozen}
          className="identity-fields"
          data-grouped={grouped || undefined}
        >
          <IdentityFieldsGroup title="Basic details" enabled={grouped}>
            <Field label="Legal name">
              <input
                name="legal_name"
                maxLength={200}
                value={legalName}
                onChange={(e) => setLegalName(e.target.value)}
              />
            </Field>
          </IdentityFieldsGroup>
          <IdentityFieldsGroup
            title={
              book.profile === "personal"
                ? "Organization details"
                : "Country and business structure"
            }
            enabled={grouped}
            collapsed={book.profile === "personal" && !country && !form}
          >
            {grouped ? (
              <IdentityCountryChoice
                label="Country"
                value={country}
                countries={catalog.countries}
                onChange={changeCountry}
              />
            ) : (
              <Field label="Formation or governing country">
                <select
                  value={knownCountry ? country : country ? "custom" : ""}
                  onChange={(e) => changeCountry(e.target.value)}
                >
                  <option value="">Unknown / not applicable</option>
                  {catalog.countries.map((c) => (
                    <option key={c.code} value={c.code}>
                      {c.name}
                    </option>
                  ))}
                  <option value="custom">Another country</option>
                </select>
              </Field>
            )}
            {!grouped && !knownCountry && country && (
              <Field
                label="Country code"
                hint="Two-letter country code, for example FR."
              >
                <input
                  required
                  maxLength={2}
                  value={country === "XX" ? "" : country}
                  onChange={(e) => setCountry(e.target.value.toUpperCase())}
                />
              </Field>
            )}
            {country &&
              (grouped ? (
                <OnboardingChoice
                  label={
                    country === "US" ? "State of formation" : "Jurisdiction"
                  }
                  name="legal_jurisdiction"
                  value={jurisdiction}
                  onChange={(value) => {
                    setJurisdiction(value === "unknown" ? "" : value);
                    setForm("");
                    setCustomForm("");
                    setOverlay("");
                    setOverlayTouched(true);
                    setDirty(true);
                  }}
                  placeholder="Choose a jurisdiction"
                  items={[
                    { id: "unknown", label: "Not set" },
                    ...jurisdictions.map((j) => ({ id: j.id, label: j.name })),
                    { id: "custom", label: "Another jurisdiction" },
                  ]}
                />
              ) : (
                <Field label="Legal jurisdiction">
                  <select
                    value={jurisdiction}
                    onChange={(e) => {
                      setJurisdiction(e.target.value);
                      setForm("");
                      setOverlay("");
                    }}
                  >
                    <option value="">Unknown / not applicable</option>
                    {jurisdictions.map((j) => (
                      <option key={j.id} value={j.id}>
                        {j.name}
                      </option>
                    ))}
                    <option value="custom">Custom jurisdiction</option>
                  </select>
                </Field>
              ))}
            {jurisdiction === "custom" && (
              <Field label="Custom jurisdiction">
                <input
                  name="custom_jurisdiction"
                  required
                  maxLength={200}
                  defaultValue={saved.custom_legal_jurisdiction}
                />
              </Field>
            )}
            {grouped ? (
              ((country && (jurisdiction || jurisdictions.length === 0)) ||
                form ||
                customForm) && (
                <BusinessStructureChoice
                  country={country}
                  jurisdiction={jurisdiction}
                  forms={catalog.forms}
                  form={form}
                  customForm={customForm}
                  onChange={(choice) => {
                    setForm(choice.form === "unknown" ? "" : choice.form);
                    setCustomForm(choice.customForm);
                    setOverlay("");
                    setOverlayTouched(true);
                    setDirty(true);
                  }}
                />
              )
            ) : (
              <>
                {" "}
                <Field
                  label="Legal form"
                  hint="The catalog is a sourced subset. A selected form does not verify this subject’s registration."
                >
                  <select
                    value={form}
                    onChange={(e) => {
                      setForm(e.target.value);
                      setOverlay("");
                    }}
                  >
                    <option value="">Unknown / not applicable</option>
                    {forms.map((f) => (
                      <option key={f.id} value={f.id}>
                        {f.local_name}
                        {f.aliases?.length ? ` (${f.aliases.join(", ")})` : ""}
                      </option>
                    ))}
                    <option value="custom">Custom legal form</option>
                  </select>
                </Field>
                {form === "custom" && (
                  <Field label="Custom legal form">
                    <input
                      name="custom_form"
                      required
                      maxLength={200}
                      defaultValue={saved.custom_legal_form}
                    />
                  </Field>
                )}
              </>
            )}
            {overlays.length > 0 && (
              <Field label="Company designation">
                <select
                  value={overlay}
                  onChange={(e) => {
                    setOverlay(e.target.value);
                    setOverlayTouched(true);
                  }}
                >
                  <option value="">None / unknown</option>
                  {overlays.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
                </select>
              </Field>
            )}
          </IdentityFieldsGroup>
          <IdentityFieldsGroup
            title="Additional identity details"
            enabled={grouped}
            collapsed
          >
            <Field label="Registration status">
              <select
                name="registration_status"
                defaultValue={saved.registration_status || ""}
              >
                <option value="">Not set</option>
                <option value="registered">Registered</option>
                <option value="not_registered">Not formally registered</option>
                <option value="unknown">Not sure</option>
              </select>
            </Field>
            <Field label={grouped ? "Identity type" : "Subject kind"}>
              <select value={kind} onChange={(e) => setKind(e.target.value)}>
                <option value="">Unknown / not classified</option>
                {[
                  ["person", "Person"],
                  ["household", "Household"],
                  ["legal_entity", "Legal entity"],
                  ["trust", "Trust"],
                  ["estate", "Estate"],
                  ["fund", "Fund"],
                  ["informal_activity", "Informal activity"],
                  ["other", "Other"],
                ].map(([id, label]) => (
                  <option key={id} value={id}>
                    {label}
                  </option>
                ))}
              </select>
            </Field>
          </IdentityFieldsGroup>
          <IdentityFieldsGroup
            title="Registrations"
            enabled={grouped}
            collapsed
          >
            <div className="form-grid">
              <Field label="Registration authority (optional)">
                <input
                  name="registration_authority"
                  maxLength={200}
                  defaultValue={saved.registrations?.[0]?.authority}
                />
              </Field>
              <Field label="Registration number (optional)">
                <input
                  name="registration_number"
                  maxLength={200}
                  defaultValue={saved.registrations?.[0]?.number}
                />
              </Field>
            </div>
            {grouped ? (
              <IdentityCountryChoice
                label="Registration country"
                name="registration_country"
                value={registrationCountry}
                countries={catalog.countries}
                onChange={(value) => {
                  setRegistrationCountry(value);
                  setDirty(true);
                }}
              />
            ) : (
              <Field label="Registration country code (optional)">
                <input
                  name="registration_country"
                  maxLength={2}
                  defaultValue={saved.registrations?.[0]?.country_code}
                />
              </Field>
            )}
          </IdentityFieldsGroup>
          <IdentityFieldsGroup title="Tax details" enabled={grouped} collapsed>
            {taxChoices.length > 0 && (
              <Field
                label="Separate tax classification"
                hint="Owner-reported fact. Vult does not determine eligibility or file an election."
              >
                <select
                  value={tax}
                  onChange={(e) => {
                    setTax(e.target.value);
                    setTaxTouched(true);
                  }}
                >
                  <option value="">Unknown / not supplied</option>
                  {taxChoices.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.name}
                    </option>
                  ))}
                </select>
              </Field>
            )}
            {grouped ? (
              <TaxResidenceChoices
                values={residences}
                countries={catalog.countries}
                onChange={(values) => {
                  setResidences(values);
                  setDirty(true);
                }}
              />
            ) : (
              <>
                {" "}
                <Field
                  label="Tax residence country codes"
                  hint="Optional owner-supplied facts, separated by commas. Independent of formation country."
                >
                  <input
                    name="tax_residence"
                    maxLength={80}
                    defaultValue={saved.tax_residence_country_codes?.join(", ")}
                  />
                </Field>
              </>
            )}
          </IdentityFieldsGroup>
        </fieldset>
      </div>
      <div className="identity-review-fields" hidden={grouped && !reviewing}>
        {grouped && reviewing && review && (
          <IdentityChangeReview
            subject={subject}
            next={review}
            catalog={catalog}
          />
        )}
        <fieldset
          className="identity-fields"
          data-grouped={grouped || undefined}
          disabled={busy || frozen || (grouped && !reviewing)}
        >
          <IdentityFieldsGroup
            title={grouped ? "Change details" : "Record a change"}
            enabled={grouped}
          >
            <Field
              label={
                grouped ? "Reason for change" : "Reason for identity change"
              }
            >
              <input
                name="reason"
                required
                maxLength={1000}
                defaultValue={pending?.reason}
                placeholder={
                  grouped
                    ? "What changed?"
                    : "Owner classification, correction, or legal conversion"
                }
              />
            </Field>
            <Field
              label={
                grouped
                  ? "Effective date (optional)"
                  : "Effective date (if known)"
              }
              hint={
                grouped
                  ? undefined
                  : "Leave blank when unknown. The recorded time is saved separately."
              }
            >
              <input
                name="effective_date"
                type="date"
                defaultValue={pending?.effective_date}
              />
            </Field>
          </IdentityFieldsGroup>
        </fieldset>
      </div>
      {frozen && <Notice tone="caution">Retry to confirm these changes.</Notice>}
      <ErrorMessage
        message={error}
        pending={busy}
        intent="write"
        retained="form"
      />
      <div className="dialog-footer">
        {grouped && (
          <Button
            color="tertiary"
            disabled={busy || frozen}
            onClick={() => (reviewing ? setReviewing(false) : onCancel?.())}
          >
            {reviewing ? "Back" : "Cancel"}
          </Button>
        )}
        <Button
          type="submit"
          busy={busy}
          disabled={
            busy ||
            (grouped && !reviewing && !dirty) ||
            (grouped &&
              reviewing &&
              !frozen &&
              !!review &&
              !identitySettingsChanges(
                subject.kind,
                subject.identity,
                review.kind ?? null,
                review.identity,
                catalog,
              ).length)
          }
        >
          {frozen
            ? "Retry identity change"
            : grouped
              ? reviewing
                ? "Save changes"
                : "Review changes"
              : "Save identity"}
        </Button>
      </div>
    </form>
  );
}

function IdentitySummary({
  subject,
  catalog,
}: {
  subject: AccountingSubject;
  catalog: LegalCatalog;
}) {
  const facts = identitySettingsFacts(
    subject.kind,
    subject.identity,
    catalog,
  ).filter((row) => row.value);
  return (
    <div className="settings-identity-summary">
      {facts.length ? (
        facts.map((row) => (
          <SettingsRow key={row.key} label={row.label}>
            <span>{row.value}</span>
          </SettingsRow>
        ))
      ) : (
        <SettingsRow label="Details">
          <span className="text-tertiary">No legal details added.</span>
        </SettingsRow>
      )}
    </div>
  );
}

function IdentityChangeReview({
  subject,
  next,
  catalog,
}: {
  subject: AccountingSubject;
  next: Change;
  catalog: LegalCatalog;
}) {
  const changes = identitySettingsChanges(
    subject.kind,
    subject.identity,
    next.kind ?? null,
    next.identity,
    catalog,
  );
  return (
    <section
      className="settings-identity-review"
      aria-label="Review identity changes"
    >
      <h3 className="vult-type-compact-title">Review changes</h3>
      <dl>
        {changes.map((row) => (
          <div key={row.key}>
            <dt>{row.label}</dt>
            <dd>
              <span className="text-tertiary">{row.before}</span>
              <span aria-hidden> → </span>
              <span>{row.value}</span>
            </dd>
          </div>
        ))}
      </dl>
      {!changes.length && <p className="muted">No changes to these details.</p>}
    </section>
  );
}

function TaxResidenceChoices({
  values,
  countries,
  onChange,
}: {
  values: string[];
  countries: LegalCatalog["countries"];
  onChange: (values: string[]) => void;
}) {
  return (
    <div className="settings-tax-residences">
      {values.length > 0 && (
        <ul>
          {values.map((code) => (
            <li key={code}>
              <span>
                {countries.find((c) => c.code === code)?.name || code}
              </span>
              <Button
                color="tertiary"
                size="sm"
                aria-label={`Remove ${countries.find((c) => c.code === code)?.name || code}`}
                onClick={() => onChange(values.filter((c) => c !== code))}
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
      )}
      <IdentityCountryChoice
        label="Tax residence"
        value=""
        countries={countries.filter((c) => !values.includes(c.code))}
        onChange={(code) => {
          if (code && !values.includes(code)) onChange([...values, code]);
        }}
      />
    </div>
  );
}

function IdentityFieldsGroup({
  title,
  enabled,
  collapsed = false,
  children,
}: {
  title: string;
  enabled: boolean;
  collapsed?: boolean;
  children: ReactNode;
}) {
  if (!enabled) return <>{children}</>;
  if (collapsed)
    return (
      <SettingsDisclosure title={title} className="book-settings-identity-group">
        <div className="book-settings-identity-group-fields">{children}</div>
      </SettingsDisclosure>
    );
  return (
    <section className="book-settings-identity-group" aria-label={title}>
      <h3 className="vult-type-label">{title}</h3>
      <div className="book-settings-identity-group-fields">{children}</div>
    </section>
  );
}

function IdentityHistoryList({
  book,
  initial,
}: {
  book: Book;
  initial: IdentityView;
}) {
  const [view, setView] = useState(initial);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  return (
    <div className="identity-history">
      <h4>Identity history</h4>
      {view.history.length === 0 && (
        <p className="muted">No classification recorded.</p>
      )}
      {view.history.map((h) => (
        <div key={h.id} className="identity-history-entry">
          <strong>
            {identityLabel(h.prior_identity)} → {identityLabel(h.new_identity)}
          </strong>
          <p>{h.reason}</p>
          <details>
            <summary>View prior and new facts</summary>
            <div className="form-grid">
              <div>
                <h4>Before</h4>
                <IdentityFacts
                  kind={h.prior_kind}
                  identity={h.prior_identity}
                />
              </div>
              <div>
                <h4>After</h4>
                <IdentityFacts kind={h.new_kind} identity={h.new_identity} />
              </div>
            </div>
          </details>
          <p className="small muted">
            Recorded {formatDateTime(h.recorded_at)} · {h.actor}
            <br />
            Effective date:{" "}
            {h.effective_date
              ? formatDate(h.effective_date)
              : "Unknown / not supplied"}{" "}
            · Catalog {h.new_identity.catalog_version || "Not classified"}
          </p>
        </div>
      ))}
      <ErrorMessage
        message={error}
        pending={busy}
        intent="write"
        retained="form"
      />
      {view.has_more && (
        <Button
          color="secondary"
          disabled={busy}
          busy={busy}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              const page = await get<IdentityView>(
                bookURL(`book-identity?offset=${view.next_offset}`, book.id),
              );
              setView({ ...page, history: [...view.history, ...page.history] });
            } catch (e) {
              setError(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          More history
        </Button>
      )}
    </div>
  );
}

function IdentityFacts({
  kind,
  identity,
}: {
  kind: string | null;
  identity: LegalIdentity;
}) {
  const facts = [
    [
      "Registration status",
      identity.registration_status === "registered"
        ? "Registered (owner reported)"
        : identity.registration_status === "not_registered"
          ? "Not formally registered"
          : identity.registration_status === "unknown"
            ? "Not sure"
            : "Not supplied",
    ],
    ["Subject kind", kind?.replaceAll("_", " ") || "Unknown"],
    [
      "Formation / governing country",
      identity.country?.name ||
        identity.formation_or_governing_country_code ||
        "Unknown / not supplied",
    ],
    [
      "Legal jurisdiction",
      identity.jurisdiction?.name ||
        identity.custom_legal_jurisdiction ||
        "Unknown / not supplied",
    ],
    ["Legal form", identityLabel(identity)],
    [
      "Designation",
      [
        ...(identity.overlays?.map((o) => o.name) || []),
        ...(identity.custom_overlays || []),
      ].join(", ") || "Not supplied",
    ],
    [
      "Tax classification",
      [
        ...(identity.tax_classifications?.map((t) => t.name) || []),
        ...(identity.custom_tax_classifications || []),
      ].join(", ") || "Not supplied",
    ],
    [
      "Tax residence",
      identity.tax_residence_country_codes?.join(", ") || "Not supplied",
    ],
    [
      "Registrations",
      identity.registrations
        ?.map(
          (r) =>
            `${r.authority}: ${r.number}${r.country_code ? ` (${r.country_code})` : ""}${r.label ? ` · ${r.label}` : ""}`,
        )
        .join("; ") || "Not supplied",
    ],
    ["Catalog version", identity.catalog_version || "Not classified"],
  ];
  return (
    <dl className="details">
      {facts.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
