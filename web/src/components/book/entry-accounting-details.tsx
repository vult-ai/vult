import {
  currencyMoney,
  money,
  type Book,
  type Currency,
  type Detail,
  type State,
} from "../../lib/api";
import { EntryPostings } from "./entry-identity";
import { sharedMemo } from "../../lib/entry-overview";

/** Every posting remains inspectable; display codes never replace its reference. */
export function EntryAccountingDetails({
  data,
  accounts,
  book,
  currencies,
}: {
  data: Detail;
  accounts: State["accounts"];
  book: Book;
  currencies: Currency[];
}) {
  const chart = new Map(accounts.map((account) => [account.code, account]));
  // A memo every posting shares is shown once, in the summary.
  const shared = sharedMemo(data.lines, data.description);
  return (
    <EntryPostings
      bookId={book.id}
      postings={data.lines.map((line) => {
        const account = chart.get(line.account_code);
        const memo = !shared && line.memo && line.memo.trim() !== data.description.trim() ? line.memo : "";
        const native =
          line.native_currency &&
          (line.native_currency !== book.currency || line.native_amount_minor !== line.amount_minor);
        return {
          key: line.id,
          code: line.account_code,
          // Names are recorded whole; separators never establish parents.
          name: line.account_name || account?.name || "Account unavailable",
          glCode: account?.gl_code?.trim() || undefined,
          side: line.side === "debit" ? "debit" : "credit",
          amount: money(line.amount_minor, book),
          extra: (memo || native) ? (
            <>
              {memo && <small className="preserve-space">{memo}</small>}
              {native && (
                <small>
                  <span className="vult-money">
                    {currencyMoney(line.native_amount_minor!, line.native_currency!, currencies)}
                  </span>{" "}
                  original
                </small>
              )}
            </>
          ) : undefined,
        };
      })}
    />
  );
}
