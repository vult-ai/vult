import { useEffect, useId, useRef, useState } from "react";
import { Check, Copy01 } from "../../design-system/icons";
import { get, type Book, type BookList } from "../../lib/api";
import { settingsPost as post } from "../../lib/settings-requests";
import {
  bookAddress,
  updateBookAddress,
  hydrateBookCatalog,
  canonicalBookURL,
  readBookRoute,
  type BookAddress,
} from "../../lib/book-routing";
import { ErrorMessage } from "../form";
import { Button } from "../ui/button";
import { SettingsRow, SettingsSection } from "../ui/settings-section";
import { BookURLInput } from "../ui/book-url-input";
import "./book-address-settings.css";
import { Notice } from "../ui/notice";
import { announce } from "../../lib/announce";

export function BookAddressSettings({ book }: { book: Book }) {
  const initial = bookAddress(book);
  return initial ? (
    <BookAddressEditor key={book.id} book={book} initial={initial} />
  ) : null;
}

/** One section: the Book's address, saved from the section header, and its fixed ID. */
function BookAddressEditor({
  book,
  initial,
}: {
  book: Book;
  initial: BookAddress;
}) {
  const [address, setAddress] = useState(initial);
  const [slug, setSlug] = useState(address?.slug || "");
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>();
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyFallback, setCopyFallback] = useState(false);
  const slugId = useId();
  const formId = useId();
  const codeRef = useRef<HTMLSpanElement>(null);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const generation = useRef(0);
  useEffect(
    () => () => {
      generation.current++;
      clearTimeout(copyTimer.current);
    },
    [book.id],
  );
  if (!address) return null;
  const copyCode = () => {
    const seq = generation.current;
    const selectCode = () => {
      if (seq !== generation.current) return;
      setCopied(false);
      setCopyFallback(true);
      announce("Book ID selected for copying.");
      if (codeRef.current) {
        const range = document.createRange();
        range.selectNodeContents(codeRef.current);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
      }
    };
    const done = () => {
      if (seq !== generation.current) return;
      setCopyFallback(false);
      setCopied(true);
      announce("Book ID copied.");
      clearTimeout(copyTimer.current);
      copyTimer.current = setTimeout(() => setCopied(false), 1800);
    };
    if (!navigator.clipboard?.writeText) {
      selectCode();
      return;
    }
    try {
      void navigator.clipboard.writeText(address.code).then(done, selectCode);
    } catch {
      selectCode();
    }
  };
  const changed = slug !== address.slug;
  return (
    <SettingsSection
      title="Address"
      description="Where this Book opens. Earlier addresses keep working."
      actions={
        <Button
          type="submit"
          form={formId}
          size="sm"
          disabled={pending || !changed}
          busy={pending}
        >
          Save
        </Button>
      }
    >
      <form
        id={formId}
        className="book-url-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (pending) return;
          const seq = ++generation.current;
          const request = {
            book_id: book.id,
            slug,
            revision: address.revision,
          };
          setPending(true);
          setFailure(undefined);
          setSaved(false);
          void post<BookAddress>(
            `book-address?book_id=${encodeURIComponent(book.id)}`,
            request,
          )
            .then((next) => {
              if (seq !== generation.current) return;
              setAddress(next);
              setSlug(next.slug);
              setSaved(true);
              // The confirmed response updates navigation without a second save or read.
              updateBookAddress(next);
              const url = canonicalBookURL(
                { ...book, address: next },
                readBookRoute().page,
              );
              window.history.replaceState(window.history.state, "", url);
            })
            .catch((error) => {
              if (seq === generation.current) setFailure(error);
            })
            .finally(() => {
              if (seq === generation.current) setPending(false);
            });
        }}
      >
        <SettingsRow label="Book URL" htmlFor={slugId}>
          <BookURLInput
            className="book-url-control"
            id={slugId}
            aria-label="Book URL"
            name="book-slug"
            value={slug}
            placeholder={address.code}
            onChange={(event) => {
              setSlug(event.currentTarget.value);
              setSaved(false);
            }}
            disabled={pending}
          />
          <ErrorMessage
            message={failure}
            intent="write"
            resource="settings"
            onReview={async () => {
              const catalog = hydrateBookCatalog(
                await get<BookList & { addresses?: BookAddress[] }>("books"),
              );
              const latest = catalog.books.find(
                (value) => value.id === book.id,
              )?.address;
              if (latest) {
                setAddress(latest);
                if (latest.slug === slug.trim().toLowerCase()) {
                  setFailure(undefined);
                  setSlug(latest.slug);
                  setSaved(true);
                }
              }
            }}
          />
          {saved ? (
            <Notice>Address saved. Earlier links still work.</Notice>
          ) : null}
        </SettingsRow>
      </form>
      <SettingsRow
        label="Book ID"
        description="Generated when the Book was created. It can’t be changed."
        hint={
          copyFallback
            ? "The Book ID is selected. Copy it to continue."
            : undefined
        }
      >
        <div className="book-url-id">
          <span ref={codeRef} className="book-url-id-value vult-type-ui">
            {address.code}
          </span>
          <Button
            type="button"
            color="tertiary"
            size="sm"
            aria-label="Copy Book ID"
            onClick={copyCode}
          >
            {copied ? (
              <Check className="size-4" aria-hidden />
            ) : (
              <Copy01 className="size-4" aria-hidden />
            )}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      </SettingsRow>
    </SettingsSection>
  );
}
