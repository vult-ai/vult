import { useEffect, useState } from "react";
import { ChevronDown, Download01, Lock01 } from "../../design-system/icons";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { Button as ActionButton } from "../../design-system/catalog/components/vult-ui/button";
import { bookURL, type Book } from "../../lib/api";
import { fetchJSON, fetchResponse, readResponse } from "../../lib/errors";
import { withRecentSignIn } from "../../lib/recent-sign-in";
import { writeHeaders } from "../../lib/session";
import { RestoreSettings } from "../backups/restore-settings";
import { Button } from "../ui/button";
import { ErrorNotice } from "../ui/error-notice";
import { SettingsRow, SettingsSection } from "../ui/settings-section";

/** The event that tells the app its Book list changed. */
export const booksChanged = "vult-books-changed";

/** Archives a Book, or brings it back, and has the Book list read again.
 * Archiving keeps everything in it. */
export const archiveBook = async (book: string, archived: boolean) => {
  const out = await withRecentSignIn(async () =>
    readResponse<{ book_id: string; archived: boolean }>(await fetchResponse("/api/v1/books/archive", {
      method: "POST", headers: await writeHeaders(), body: JSON.stringify({ book_id: book, archived }),
    }), "write"));
  window.dispatchEvent(new Event(booksChanged));
  return out;
};

/* Archive: put a Book away, out of the Book menu, keeping everything in it.
   It comes back from Manage Books, or here, at any time. Whether it's
   archived is read from the Book list, which says so; the Book's own state
   doesn't. */
function ArchiveSetting({ book, onChanged }: { book: Book; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<unknown>(null);
  const [archived, setArchived] = useState(Boolean(book.archived));
  useEffect(() => {
    const c = new AbortController();
    fetchJSON<{ books?: { id: string; archived?: boolean }[] }>("/api/v1/books", { cache: "no-store", signal: c.signal })
      .then((list) => { const found = list.books?.find((b) => b.id === book.id); if (found) setArchived(Boolean(found.archived)); }, () => undefined);
    return () => c.abort();
  }, [book.id]);
  const run = async () => {
    setBusy(true);
    setProblem(null);
    try {
      const out = await archiveBook(book.id, !archived);
      setArchived(out.archived);
    } catch (e) {
      setProblem(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <SettingsSection title="Archive" icon={Lock01} description="Put this Book away. Everything in it stays.">
      <SettingsRow label={archived ? "Archived" : "Archive this Book"}
        description={archived ? "Out of the Book menu. Everything in it stays." : "It leaves the Book menu. You can bring it back from Manage Books at any time."}>
        <Button size="sm" color="secondary" busy={busy} disabled={busy} onClick={() => void run()}>{archived ? "Unarchive" : "Archive"}</Button>
      </SettingsRow>
      <ErrorNotice error={problem} intent="write" />
    </SettingsSection>
  );
}

/* The way out: everything in this Book, downloadable at any time, in the same
   files the terminal writes with `vult export`. Nothing here changes the Book. */
export function BookDataSettings({ book, active = true, onChanged = () => undefined }: { book: Book; active?: boolean; onChanged?: () => void }) {
  return (
    <>
    <SettingsSection
      title="Download data" icon={Download01}
      description="Open formats, anytime."
    >
      <SettingsRow
        label="Whole Book"
        description="Entries, accounts and originals."
      >
        <ActionButton href={`/api/v1/${bookURL("exports/book.tar", book.id)}`} size="md" iconLeading={Download01}>
          Download Book
        </ActionButton>
      </SettingsRow>
      <SettingsRow
        label="Entries and accounts"
        description="For spreadsheets."
      >
        <Dropdown.Root>
          <ActionButton color="secondary" size="md" iconTrailing={ChevronDown}>
            Download
          </ActionButton>
          <Dropdown.Popover appearance="control" placement="bottom start">
            <Dropdown.Menu aria-label="Download entries and accounts" selectionMode="none">
              {[
                ["journal.json", "Journal (JSON)"],
                ["transactions.csv", "Transactions (CSV)"],
                ["journal-lines.csv", "Journal lines (CSV)"],
                ["accounts.csv", "Accounts (CSV)"],
              ].map(([file, label]) => (
                <Dropdown.Item
                  key={file}
                  id={file}
                  appearance="control"
                  icon={Download01}
                  href={`/api/v1/${bookURL(`exports/${file}`, book.id)}`}
                >
                  {label}
                </Dropdown.Item>
              ))}
            </Dropdown.Menu>
          </Dropdown.Popover>
        </Dropdown.Root>
      </SettingsRow>
    </SettingsSection>
    <ArchiveSetting book={book} onChanged={onChanged} />
    <RestoreSettings active={active} />
    </>
  );
}
