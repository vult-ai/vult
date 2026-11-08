import type { ReactNode } from "react";
import {
  ArrowDownLeft,
  ArrowUpRight,
  BookOpen01,
  FileDownload02,
  FlipBackward,
  SwitchHorizontal01,
} from "../../design-system/icons";
import { Avatar } from "../../design-system/catalog/components/vult-ui/avatar";
import type { JournalRecorder } from "../../lib/journal-read";
import { profileInitials } from "../../lib/user-profile";
import { usePersonalAccount } from "../shell/personal-settings-host";
import { AccountAvatar, CounterpartyAvatar } from "../ui/account-avatar";
import { useAccountPresentation } from "../workspaces/account-presentation-controls";
import { useInstitutionCatalog } from "../../lib/use-institution-catalog";
import { activityBrand } from "../../lib/activity-brands";
import { RecordDate } from "../ui/record-date";
import { DisplayAmount } from "../ui/display-amount";
import "./entry-identity.css";

export type EntryTone = "in" | "out" | "transfer" | "neutral" | "reversal";

const toneIcon = {
  in: ArrowDownLeft,
  out: ArrowUpRight,
  transfer: SwitchHorizontal01,
  neutral: BookOpen01,
  reversal: FlipBackward,
} as const;

/** A tinted tile for the entry's kind, when there is no party or account look. */
export function EntryKindAvatar({ tone }: { tone: EntryTone }) {
  const Icon = toneIcon[tone];
  return (
    <span className="entry-kind-avatar" data-tone={tone} aria-hidden="true">
      <Icon />
    </span>
  );
}

/** Who or what the entry is about, its amount, and what kind of entry it is. */
export function EntryHero({
  avatar,
  title,
  description,
  date,
  amount,
  chip,
  tone,
  badges,
  notes,
}: {
  avatar: ReactNode;
  title: string;
  /** The recorded purpose, when the title is a party rather than the purpose. */
  description?: string;
  date: string;
  amount: string;
  chip: string;
  tone: EntryTone;
  badges?: ReactNode;
  notes?: ReactNode;
}) {
  const Icon = toneIcon[tone];
  // Shown as words; the exact recorded text stays in Record details.
  const words = (text: string) => text.replace(/\s+/gu, " ").trim();
  return (
    <header className="entry-hero">
      <div className="entry-hero-identity">
        {avatar}
        <div className="entry-hero-title">
          <h3 className="vult-type-compact-title text-primary">{words(title)}</h3>
          {description && (
            <p className="vult-type-ui text-secondary">{words(description)}</p>
          )}
        </div>
      </div>
      <div className="entry-hero-amount">
        <p className="vult-type-dashboard-value text-primary"><DisplayAmount value={amount} /></p>
        <div className="entry-hero-facts">
          <span className="entry-chip vult-type-metadata" data-tone={tone}>
            <Icon aria-hidden="true" />
            {chip}
          </span>
          <span className="vult-type-metadata text-tertiary">
            <RecordDate value={date} format="full" />
          </span>
          {badges}
        </div>
        {notes}
      </div>
    </header>
  );
}

/** An account's look as the owner set it in Accounts: name and logo. */
export function useAccountLook(bookId: string) {
  const presentation = useAccountPresentation(bookId);
  const catalog = useInstitutionCatalog(true);
  return (code: string, name: string, density: "default" | "compact" = "default") => {
    const preference = presentation.value.accounts[code];
    const label = preference?.alias || name;
    const institution = preference?.institutionId
      ? catalog.institutions.find((item) => item.id === preference.institutionId)
      : undefined;
    return {
      label,
      institution,
      avatar: (
        <AccountAvatar
          accountKey={`${bookId}:${code}`}
          name={label}
          base={catalog.base}
          institution={institution}
          density={density}
        />
      ),
    };
  };
}

/** A long account name may break after ":" or "/", never inside a word. */
function breakable(name: string) {
  return name.split(/(?<=[:/])/u).map((part, index) => (
    <span key={index}>{index > 0 && <wbr />}{part}</span>
  ));
}

export type EntryPosting = {
  key: string;
  code: string;
  name: string;
  glCode?: string;
  side: "debit" | "credit";
  amount: string;
  /** Memo and measurements recorded on the posting. */
  extra?: ReactNode;
};

/** Every posting, as money moving from the credited accounts to the debited
 * ones. Each keeps its side, code and measurements; nothing is summarized. */
export function EntryPostings({
  bookId,
  postings,
}: {
  bookId: string;
  postings: readonly EntryPosting[];
}) {
  const look = useAccountLook(bookId);
  // Credited accounts first, then debited: money moving from one to the other.
  const ordered = [
    ...postings.filter((p) => p.side === "credit"),
    ...postings.filter((p) => p.side === "debit"),
  ];
  return (
    <section className="entry-postings" aria-label="Postings">
      <ol className="entry-postings-list">
        {ordered.map((posting, index) => {
          const account = look(posting.code, posting.name, "compact");
          const first = index === 0 || ordered[index - 1].side !== posting.side;
          const side = posting.side === "debit" ? "Debit" : "Credit";
          const context = [
            posting.glCode,
            account.label !== posting.name ? posting.name : "",
          ].filter(Boolean).join(" · ");
          return (
            <li key={posting.key} className="entry-posting" data-side={posting.side} data-first={first || undefined}>
              <span className="entry-posting-label vult-type-metadata text-tertiary" title={side}>
                {first ? (posting.side === "credit" ? "From" : "To") : ""}
                <span className="sr-only">{` ${side}`}</span>
              </span>
              {account.avatar}
              <div className="entry-posting-body">
                <span className="vult-type-ui text-primary entry-posting-name" title={posting.glCode ? `${posting.glCode} · ${posting.name}` : posting.name}>
                  {breakable(account.label)}
                </span>
                {context && <span className="vult-type-metadata text-tertiary">{context}</span>}
                {posting.extra}
              </div>
              <span className="vult-type-ui vult-type-number text-primary entry-posting-amount">
                {posting.amount}
              </span>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** Attribution from the journal read, using the signed-in owner's profile. */
export function EntryRecordedBy({ recorder }: { recorder: JournalRecorder }) {
  const account = usePersonalAccount();
  const photo = account?.scope.kind === "user" ? account.profile.photo : null;
  return (
    <section className="entry-origin" aria-label="Recorder">
      {recorder.kind === "owner" ? (
        <span className="shrink-0" aria-hidden="true">
          <Avatar size="xs" src={photo} initials={profileInitials(recorder.name)} alt="" />
        </span>
      ) : (
        <CounterpartyAvatar identityKey={`connection:${recorder.connection_id}`} name={recorder.name} base="" />
      )}
      <p className="vult-type-ui text-tertiary">Recorded by {recorder.name}</p>
    </section>
  );
}

/** Where the entry came from, when the record says. */
export function EntryOrigin({
  provider,
  importedAt,
  detail,
}: {
  provider?: string;
  importedAt?: string;
  detail?: string;
}) {
  const catalog = useInstitutionCatalog(!!provider, "brands");
  const brand = provider
    ? activityBrand({ state: "proposed", name: provider }, catalog.institutions)
    : undefined;
  return (
    <section className="entry-origin" aria-label="Origin">
      {provider ? (
        <CounterpartyAvatar
          identityKey={`provider:${provider.toLowerCase()}`}
          name={provider}
          base={catalog.base}
          institution={brand}
        />
      ) : (
        <span className="entry-origin-icon" aria-hidden="true">
          <FileDownload02 />
        </span>
      )}
      <p className="vult-type-ui text-tertiary">
        {provider ? `Imported from ${provider}` : "Imported"}
        {importedAt && <> · <RecordDate value={importedAt.slice(0, 10)} format="full" /></>}
        {detail && ` · ${detail}`}
      </p>
    </section>
  );
}
