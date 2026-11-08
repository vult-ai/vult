import { useEffect, useMemo, useRef, useState, type ComponentType } from "react";
import {
  Autocomplete,
  Header,
  Input,
  ListBox,
  ListBoxItem,
  ListBoxSection,
  SearchField,
} from "react-aria-components";
import { ArrowRight, File06, SearchLg, User01, Wallet01 } from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { Kbd } from "../../design-system/catalog/components/vult-ui/kbd";
import { Dialog } from "../ui/dialog";
import { LoadingState } from "../ui/loading-state";
import { EmptyState } from "../ui/empty-state";
import { ErrorMessage } from "../form";
import { money, type Book } from "../../lib/api";
import { formatDate } from "../../lib/date-format";
import { searchBook, searchable, type BookSearchResult } from "../../lib/book-search";
import "./book-finder.css";

export type FinderPage = {
  id: string;
  label: string;
  icon: ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
};

type Choice =
  | { kind: "entry"; id: string }
  | { kind: "account"; id: string }
  | { kind: "accounts"; query: string }
  | { kind: "counterparty"; name: string }
  | { kind: "source"; query: string; id?: string }
  | { kind: "page"; id: string }
  | { kind: "all"; query: string };

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** Search standard v1 (docs/design/search.md): ⌘K finds anything in the Book
 * from any page. Typing never leaves the page; choosing a result opens it. */
export function BookFinder({
  book,
  pages,
  onOpenEntry,
  onOpenPage,
  onShowTransactions,
  onOpenAccount,
  onShowSources,
  onShowAccounts,
}: {
  book: Book;
  pages: readonly FinderPage[];
  onOpenEntry: (id: string) => void;
  onOpenPage: (id: string) => void;
  onShowTransactions: (query: string) => void;
  /** Opens one account's details on the Accounts page. */
  onOpenAccount: (id: string) => void;
  onShowSources: (query: string, id?: string) => void;
  /** Shows Accounts filtered by the search. */
  onShowAccounts: (query: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // Results with the query they answer; the server may normalize its echo.
  const [result, setResult] = useState<{ query: string; data: BookSearchResult } | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>();
  const [modifier, setModifier] = useState("⌘");
  const trigger = useRef<HTMLButtonElement>(null);
  const after = useRef<Choice | null>(null);
  // The search carried over from the last time the finder was open.
  const carried = useRef("");
  const latest = useRef(query);
  latest.current = query;
  // Captured as the finder opens, before the field takes focus.
  const setFinderOpen = (next: boolean) => {
    if (next) carried.current = latest.current;
    setOpen(next);
  };

  useEffect(() => {
    const mac = /Mac|iPhone|iPad|iPod/.test(navigator.platform);
    setModifier(mac ? "⌘" : "Ctrl");
    const keydown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || event.repeat || event.altKey || event.shiftKey) return;
      // ⌘K on a Mac, Ctrl+K elsewhere. On a Mac, Ctrl+K deletes to the end of
      // the line in a text field, so it is left alone.
      if (!(mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) || event.key.toLowerCase() !== "k") return;
      // Another dialog keeps the keyboard; the finder never stacks on top of it.
      if (!open && document.querySelector('[role="dialog"][data-state="open"], [role="alertdialog"]')) return;
      event.preventDefault();
      setFinderOpen(!open);
    };
    window.addEventListener("keydown", keydown);
    return () => window.removeEventListener("keydown", keydown);
  }, [open]);

  // React Aria's Button drops aria-keyshortcuts, so set it on the element.
  useEffect(() => {
    trigger.current?.setAttribute("aria-keyshortcuts", modifier === "⌘" ? "Meta+K" : "Control+K");
  }, [modifier]);

  // Each Book starts with an empty finder.
  useEffect(() => {
    setQuery("");
    setResult(null);
  }, [book.id]);

  const text = query.trim();
  useEffect(() => {
    setError(undefined);
    if (!open || !searchable(text)) {
      setLoading(false);
      if (!text) setResult(null);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    const timer = window.setTimeout(() => {
      searchBook(book.id, text, controller.signal)
        .then((next) => {
          if (!controller.signal.aborted) setResult({ query: text, data: next });
        })
        .catch((failure) => {
          if (!controller.signal.aborted) setError(failure);
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false);
        });
    }, 150);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [open, text, book.id]);

  const matchingPages = useMemo(
    () => pages.filter((page) => !text || page.label.toLowerCase().includes(text.toLowerCase())),
    [pages, text],
  );
  const shown = result && result.query === text ? result.data : null;
  const total = shown?.transactions.total;
  const found = shown
    ? shown.transactions.items.length + shown.accounts.items.length + shown.counterparties.items.length + shown.sources.items.length
    : 0;
  // What a screen reader hears as results change.
  const status = !text
    ? ""
    : loading
      ? "Searching this Book"
      : error
        ? ""
        : !searchable(text)
          ? "That search is too long"
          : shown
            ? found + matchingPages.length
              ? `${plural(found + matchingPages.length, "result", "results")}`
              : `No results for ${text}`
            : "";

  function choose(choice: Choice) {
    // Close first so focus returns to the trigger, then open the result.
    after.current = choice;
    setFinderOpen(false);
  }
  useEffect(() => {
    if (open || !after.current) return;
    const choice = after.current;
    after.current = null;
    const frame = requestAnimationFrame(() => {
      if (choice.kind === "entry") onOpenEntry(choice.id);
      else if (choice.kind === "account") onOpenAccount(choice.id);
      else if (choice.kind === "accounts") onShowAccounts(choice.query);
      else if (choice.kind === "counterparty") onShowTransactions(choice.name);
      else if (choice.kind === "source") onShowSources(choice.query, choice.id);
      else if (choice.kind === "page") onOpenPage(choice.id);
      else onShowTransactions(choice.query);
    });
    return () => cancelAnimationFrame(frame);
  });

  const empty = () =>
    !text ? null : loading ? (
      <LoadingState scope="inline" label="Searching this Book…" />
    ) : error ? null : !searchable(text) ? (
      <EmptyState scope="inline" title="That search is too long" detail="Use up to 8 words and 200 characters." />
    ) : (
      <EmptyState scope="inline" title={`No results for “${text}”`} detail="Try fewer words, an amount such as 1,502.55, or a date such as Oct 24." />
    );

  return (
    <>
      <Button
        ref={trigger}
        aria-label="Search this Book"
        aria-haspopup="dialog"
        aria-expanded={open}
        color="secondary"
        size="sm"
        iconLeading={SearchLg}
        className="vult-shell-search"
        noTextPadding
        onPress={() => setFinderOpen(true)}
      >
        <span aria-hidden="true" className="flex min-w-0 items-center gap-0.5">
          <span>Search</span>
          <span className="ml-1 hidden items-center gap-0.5 sm:flex">
            <Kbd>{modifier}</Kbd>
            <Kbd>K</Kbd>
          </span>
        </span>
      </Button>
      <Dialog
        title="Search this Book"
        description={null}
        size="medium"
        className="book-finder"
        open={open}
        onOpenChange={setFinderOpen}
        returnTo={trigger.current}
      >
        <Autocomplete inputValue={query} onInputChange={setQuery}>
          <SearchField className="book-finder-field vult-focus-within" aria-label="Search this Book" autoFocus maxLength={200}>
            <SearchLg className="book-finder-field-icon" aria-hidden />
            <Input
              // Reopening keeps the last search to return to, selected so typing
              // replaces it. Text typed since opening is never selected.
              onFocus={(event) => {
                if (carried.current && event.currentTarget.value === carried.current) event.currentTarget.select();
                carried.current = "";
              }}
              className="book-finder-input vult-type-body"
              placeholder="Transactions, amounts, accounts, sources…"
            />
            <Kbd>esc</Kbd>
          </SearchField>
          <div role="status" className="sr-only">{status}</div>
          {/* A failed search sits beside what still works: the field and Go to. */}
          {!!error && (
            <div className="book-finder-error">
              <ErrorMessage message={error} intent="read" resource="search" />
            </div>
          )}
          <ListBox
            aria-label="Search results"
            className="book-finder-results"
            renderEmptyState={empty}
          >
            {shown && shown.transactions.items.length > 0 && (
              <ListBoxSection className="book-finder-section">
                <Header className="book-finder-heading vult-type-metadata">Transactions</Header>
                {shown.transactions.items.map((t) => (
                  <ListBoxItem
                    key={t.id}
                    id={`entry:${t.id}`}
                    textValue={`${t.counterparty || t.description} ${t.date}`}
                    className="book-finder-option"
                    onAction={() => choose({ kind: "entry", id: t.id })}
                  >
                    <span className="book-finder-option-main">
                      <span className="book-finder-option-title vult-type-ui">{t.counterparty || t.description}</span>
                      <span className="book-finder-option-meta vult-type-metadata">
                        {formatDate(t.date)}
                        {t.accounts?.length ? ` · ${t.accounts.at(-1)}` : ""}
                      </span>
                    </span>
                    <span className="book-finder-amount vult-type-ui vult-type-number">{money(t.amount_minor, book)}</span>
                  </ListBoxItem>
                ))}
                {total !== undefined && (total === null || total > shown.transactions.items.length) && (
                  <ListBoxItem
                    id="all"
                    textValue={`Show all transactions matching ${text}`}
                    className="book-finder-option book-finder-all"
                    onAction={() => choose({ kind: "all", query: text })}
                  >
                    <span className="book-finder-option-title vult-type-ui">
                      {total === null ? "Show all matching transactions" : `Show all ${plural(total, "transaction", "transactions")}`}
                    </span>
                    <ArrowRight className="book-finder-option-icon" aria-hidden />
                  </ListBoxItem>
                )}
              </ListBoxSection>
            )}
            {shown && shown.accounts.items.length > 0 && (
              <ListBoxSection className="book-finder-section">
                <Header className="book-finder-heading vult-type-metadata">Accounts</Header>
                {shown.accounts.items.map((a) => (
                  <ListBoxItem
                    key={a.id}
                    id={`account:${a.id}`}
                    textValue={`${a.code} ${a.name}`.trim()}
                    className="book-finder-option"
                    onAction={() => choose({ kind: "account", id: a.id })}
                  >
                    <Wallet01 className="book-finder-option-icon" aria-hidden />
                    <span className="book-finder-option-main">
                      <span className="book-finder-option-title vult-type-ui">
                        {a.code ? <span className="book-finder-code vult-type-number">{a.code}</span> : null}
                        {a.name}
                      </span>
                    </span>
                  </ListBoxItem>
                ))}
                {(shown.accounts.total === null || shown.accounts.total > shown.accounts.items.length) && (
                  <ListBoxItem
                    id="all-accounts"
                    textValue={`Show all accounts matching ${text}`}
                    className="book-finder-option book-finder-all"
                    onAction={() => choose({ kind: "accounts", query: text })}
                  >
                    <span className="book-finder-option-title vult-type-ui">
                      {shown.accounts.total === null ? "Show all matching accounts" : `Show all ${plural(shown.accounts.total, "account", "accounts")}`}
                    </span>
                    <ArrowRight className="book-finder-option-icon" aria-hidden />
                  </ListBoxItem>
                )}
              </ListBoxSection>
            )}
            {shown && shown.counterparties.items.length > 0 && (
              <ListBoxSection className="book-finder-section">
                <Header className="book-finder-heading vult-type-metadata">People and businesses</Header>
                {shown.counterparties.items.map((c) => (
                  <ListBoxItem
                    key={c.name}
                    id={`counterparty:${c.name}`}
                    textValue={c.name}
                    className="book-finder-option"
                    onAction={() => choose({ kind: "counterparty", name: c.name })}
                  >
                    <User01 className="book-finder-option-icon" aria-hidden />
                    <span className="book-finder-option-main">
                      <span className="book-finder-option-title vult-type-ui">{c.name}</span>
                    </span>
                    <span className="book-finder-option-meta vult-type-metadata">{plural(c.entries, "entry", "entries")}</span>
                  </ListBoxItem>
                ))}
              </ListBoxSection>
            )}
            {shown && shown.sources.items.length > 0 && (
              <ListBoxSection className="book-finder-section">
                <Header className="book-finder-heading vult-type-metadata">Sources</Header>
                {shown.sources.items.map((s) => (
                  <ListBoxItem
                    key={s.id}
                    id={`source:${s.id}`}
                    textValue={s.name}
                    className="book-finder-option"
                    onAction={() => choose({ kind: "source", query: text, id: s.id })}
                  >
                    <File06 className="book-finder-option-icon" aria-hidden />
                    <span className="book-finder-option-main">
                      <span className="book-finder-option-title vult-type-ui">{s.name}</span>
                      <span className="book-finder-option-meta vult-type-metadata">
                        {[s.kind, s.date && formatDate(s.date)].filter(Boolean).join(" · ")}
                      </span>
                    </span>
                  </ListBoxItem>
                ))}
                {(shown.sources.total === null || shown.sources.total > shown.sources.items.length) && (
                  <ListBoxItem
                    id="all-sources"
                    textValue={`Show all sources matching ${text}`}
                    className="book-finder-option book-finder-all"
                    onAction={() => choose({ kind: "source", query: text })}
                  >
                    <span className="book-finder-option-title vult-type-ui">
                      {shown.sources.total === null ? "Show all matching sources" : `Show all ${plural(shown.sources.total, "source", "sources")}`}
                    </span>
                    <ArrowRight className="book-finder-option-icon" aria-hidden />
                  </ListBoxItem>
                )}
              </ListBoxSection>
            )}
            {matchingPages.length > 0 && (
              <ListBoxSection className="book-finder-section">
                <Header className="book-finder-heading vult-type-metadata">Go to</Header>
                {matchingPages.map(({ id, label, icon: Icon }) => (
                  <ListBoxItem
                    key={id}
                    id={`page:${id}`}
                    textValue={label}
                    className="book-finder-option"
                    onAction={() => choose({ kind: "page", id })}
                  >
                    <Icon className="book-finder-option-icon" aria-hidden />
                    <span className="book-finder-option-main">
                      <span className="book-finder-option-title vult-type-ui">{label}</span>
                    </span>
                  </ListBoxItem>
                ))}
              </ListBoxSection>
            )}
          </ListBox>
        </Autocomplete>
      </Dialog>
    </>
  );
}
