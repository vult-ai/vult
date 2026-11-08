# Search

Version 2. This standard covers finding things in a Book: the finder that
opens with ⌘K (Ctrl+K on Windows and Linux) from any page, and how a page
shows the search it is filtered by. The matching
rules belong to the ledger's search (`ledger/search.go`): the finder,
Activity and its summary, Accounts, Sources, and agents through MCP
(`search_book`) all use it, so people and agents find the same records.

## Principle

**One way to find anything, and typing never moves you.** The finder searches
the whole Book from wherever you are and shows what it found in place.
Nothing changes on the page until you choose a result. No page has a search
field of its own: the finder is how you search transactions, accounts and
files, and "Show all" brings that search to the page.

## The finder

* **Opening.** ⌘K on a Mac, Ctrl+K elsewhere, toggles it, as does Search in
  the page bar. On a Mac, Ctrl+K is left to text fields, where it deletes to
  the end of the line. It never opens on top of another dialog. It is a
  medium dialog titled "Search this Book" that grows with its results, up to
  the dialog's height limit.
* **The field.** It has focus on opening. A search from last time is kept and
  selected, so typing replaces it and arrow keys return to it. Text typed
  since opening is never selected.
* **Results**, in this order, each group under its heading. A group with no
  results is not shown.
  1. **Transactions**: the five best matches, then "Show all N transactions"
     when there are more.
  2. **Accounts**: the five best matches, then "Show all N accounts" when
     there are more.
  3. **People and businesses**, with how many entries each has. The ledger
     doesn't record counterparties yet, so the search returns this group
     empty, marked `not_recorded`, and the finder never shows it. A name in a
     description is found as a transaction, never inferred as a person.
  4. **Sources**: the five best matches, then "Show all N sources" when
     there are more.
  5. **Go to**: the Book's pages, filtered by name. This group is there before
     anything is typed and whenever the search fails.
* **Rows.** Each row is 40px or taller: a title, then metadata (date ·
  account, or kind · date). A transaction's title is its description.
* **Amounts.** A transaction's amount ends its row in number type. It is the
  entry's Book-currency debit total (`amount_role: "book_debit_total"`), the
  same measure Activity shows, and it is unsigned: a search result never
  claims money came in or went out.
* **Keyboard.** Up and Down move through every group as one list; the first
  result is active. Enter opens it. Escape closes the finder.
* **Choosing.** The finder closes, focus returns to Search, and then:

  | Result | Opens |
  | -- | -- |
  | A transaction | Its details, beside Activity |
  | Show all | Activity, filtered by the search |
  | An account | Accounts, with that account's details open |
  | Show all accounts | All matching accounts, including accounts with no entries |
  | A source | Sources, with that original selected |
  | Show all sources | Sources, filtered by the search |
  | A page | That page |

* **Screen readers** hear the state as it changes: "Searching this Book",
  "11 results", "No results for …".
* **States**, from the [feedback standard](feedback.md), inline in the
  results: "Searching this Book…" while waiting; "No results for “…”" with a
  suggestion to try fewer words, an amount or a date; "That search is too
  long" past 8 words or 200 characters. A failed search shows an inline
  error under the field ("We couldn't load search results"), beside Go to,
  which still works.

## On the page

The search a page is filtered by appears in its toolbar, as a filter: the
search in quotes, with × to remove it (`SearchFilter` from
`design-system/search-filter.tsx`). On Activity it follows the view tabs; on
Sources it follows the title; in the account tree, too narrow to share a row,
it takes its own row under the title. The list, its count and Activity's
summary all follow it. To change the search, search again from the finder.

Accounts uses the matching account IDs from the ledger. A new search opens
All accounts, so it includes matches without entries. The ordinary view
returns when the search is cleared. Sources reads one matching page and the
total for the Book. Additional pages load when requested.

## In the address

The open collection's search is in the address as `?q=`, so a search survives
a reload, Back and Forward, and can be shared as a link. Opening a page with
`?q=`, or going Back or Forward to one, reads the collection with that
search.
The finder's own text is never put in the address. Opening an account from
the finder passes `?account=` to Accounts, which opens it and then drops it
from the address.

## What matches

In short (the ledger's search has the full rules and test cases):

* Every term must match, in any order. Quoted text matches as consecutive
  words. Punctuation separates words, so "payroll salaries" finds
  Payroll:Salaries & Wages. A search of only punctuation filters nothing.
* Case and accents are ignored. A word matches the start of a word, so "amaz"
  finds Amazon. Numbers and single letters match whole words only, so "0"
  doesn't find "0140".
* A word with a small typo ("amazn") matches, but only when nothing in the
  Book matched it as typed.
* An amount, written any common way ("1,502.55", "$1502.55"), matches entries
  of exactly that amount, or with a line of that amount. Numbers also match
  as text, such as a card's last four digits.
* Dates match as "2026-10-24", "Oct 24", "Oct 24 2019", "Oct 2026", "October"
  or "2026". Without a year, a month or day means the most recent one in the
  Book's timezone.
* Results rank by each record's weakest term: the accounts an entry posts
  to, its date and its own amount first; then its description, memos and
  line amounts; then typos. Newest first within a rank.

## Check

`scripts/check-search-standard.mjs`, part of `npm run design:check`, fails
when:

* `SearchField` is used anywhere but the finder;
* a collection toolbar draws its own `type="search"` input;
* anything but the finder binds ⌘K or Ctrl+K.

Search inside a field, such as the currency picker's filter, is part of that
field and follows the [form standard](forms.md).
