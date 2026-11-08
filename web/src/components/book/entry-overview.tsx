import {
  currencyMoney,
  money,
  type Book,
  type Currency,
  type Detail,
  type State,
} from "../../lib/api";
import { entryOverview, sharedMemo } from "../../lib/entry-overview";
import { counterpartyAvatarIdentity, counterpartyIdentity } from "../../lib/counterparty-identity";
import { activityBrand } from "../../lib/activity-brands";
import { useInstitutionCatalog } from "../../lib/use-institution-catalog";
import { GLCodeField } from "./gl-code-field";
import { AccountAvatar } from "../ui/account-avatar";
import { EntryHero, EntryKindAvatar, useAccountLook, type EntryTone } from "./entry-identity";

/** Who the entry is about, its amount and kind. Postings, sources and the full record follow. */
export function EntryOverview({
  data,
  state,
  book,
  currencies,
}: {
  data: Detail;
  state: State;
  book: Book;
  currencies: Currency[];
}) {
  const party = counterpartyIdentity(data.counterparty);
  const partyName = data.counterparty?.trim();
  const catalog = useInstitutionCatalog(!!data.brand || !!party.institutionId, "brands");
  const overview = entryOverview(data, state);
  const foreign =
    data.fx.original_currency !== book.currency ||
    data.fx.original_amount_minor !== data.amount_minor;
  const separatePaid =
    data.fx.paid_amount_minor &&
    data.fx.paid_currency &&
    (data.fx.paid_currency !== book.currency ||
      data.fx.paid_amount_minor !== data.amount_minor) &&
    (data.fx.paid_currency !== data.fx.original_currency ||
      data.fx.paid_amount_minor !== data.fx.original_amount_minor);
  const look = useAccountLook(book.id);
  const institution = activityBrand(data.brand, catalog.institutions) || (data.brand ? undefined : catalog.institutions.find(
    (item) => item.id === party.institutionId,
  ));
  const identified = !!partyName || !!institution || data.brand?.state === "proposed";
  const tone: EntryTone =
    data.kind === "reversal"
      ? "reversal"
      : overview.direction === "in" || overview.direction === "transfer" || overview.direction === "neutral"
        ? overview.direction
        : "out";
  // The party when the record names one; otherwise the bank or card account
  // the money moved through; otherwise the kind of entry.
  const account = overview.accounts[0];
  const identity = counterpartyAvatarIdentity({ brand: data.brand, institution, counterparty: data.counterparty });
  const avatar = identified ? (
    <AccountAvatar
      accountKey={identity.key}
      name={identity.name}
      base={catalog.base}
      institution={institution}
      pending={(!data.brand || ["identified", "proposed"].includes(data.brand.state)) &&
        catalog.status === "loading" && !catalog.institutions.length}
    />
  ) : account ? look(account.code, account.name).avatar : <EntryKindAvatar tone={tone} />;
  const title = partyName || identity.name || data.description;
  const displayFacts = Boolean(
    overview.category ||
    (data.payer === "founder" && (data.view_kind || data.kind) === "expense") ||
    (data.kind !== "journal" && (foreign || separatePaid)) ||
    data.import?.document_number,
  );
  return (
    <section className="entry-overview" aria-label="Transaction summary">
      <EntryHero
        avatar={avatar}
        title={title}
        description={[
          data.description && data.description !== title ? data.description : "",
          sharedMemo(data.lines, data.description),
        ].filter((text) => text && text !== title).join(" · ") || undefined}
        date={data.date}
        amount={money(data.amount_minor, book)}
        chip={overview.label}
        tone={tone}
        badges={data.reversed_by ? (
          <span className="badge">Reversed</span>
        ) : data.replacement_of ? (
          <span className="badge">Correction</span>
        ) : null}
        notes={data.fx.selection === "reference" ? (
          <p className="vult-type-metadata text-tertiary">Estimated book value</p>
        ) : data.fx.selection === "manual" ? (
          <p className="vult-type-metadata text-tertiary">Manual exchange rate</p>
        ) : null}
      />
      {displayFacts && (
        <dl className="entry-overview-facts vult-type-ui">
          {overview.category && (
            <>
              <dt>Category</dt>
              <dd>
                <GLCodeField
                  book={book.id}
                  account={overview.categoryAccount}
                  value={overview.category}
                  title={overview.categoryTitle}
                  wrap
                />
              </dd>
            </>
          )}
          {data.payer === "founder" &&
            (data.view_kind || data.kind) === "expense" && (
              <>
                <dt>Paid by</dt>
                <dd>Founder personally</dd>
              </>
            )}
          {foreign && data.kind !== "journal" && (
            <>
              <dt>Original</dt>
              <dd>
                <span className="vult-money">
                  {currencyMoney(
                    data.fx.original_amount_minor,
                    data.fx.original_currency,
                    currencies,
                  )}
                </span>
              </dd>
            </>
          )}
          {separatePaid && data.kind !== "journal" && (
            <>
              <dt>Paid / received</dt>
              <dd>
                <span className="vult-money">
                  {currencyMoney(
                    data.fx.paid_amount_minor,
                    data.fx.paid_currency,
                    currencies,
                  )}
                </span>
              </dd>
            </>
          )}
          {data.import?.document_number && (
            <>
              <dt>Reference</dt>
              <dd>{data.import.document_number}</dd>
            </>
          )}
        </dl>
      )}
    </section>
  );
}
