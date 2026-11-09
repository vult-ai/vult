import { useRef, useState, type ReactNode } from "react";
import { type Book } from "../../lib/api";
import { browserTimeZone } from "../../lib/date-format";
import { settingsPost } from "../../lib/settings-requests";
import { ErrorMessage } from "../form";
import { AccountingTimezone } from "../onboarding/accounting-timezone";
import { PurposeChoice } from "../onboarding/welcome";
import { Button } from "../ui/button";

/* A Book created without its type or accounting timezone records each once,
   here, through the same recorded settings revision as its name. After that
   they are fixed. */
function SetupForm({
  book,
  field,
  value,
  action,
  onSaved,
  children,
}: {
  book: Book;
  field: "profile" | "accounting_timezone";
  value: string;
  action: string;
  onSaved: () => void;
  children: ReactNode;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const key = useRef(crypto.randomUUID());
  async function save() {
    setBusy(true);
    setError(null);
    try {
      await settingsPost<Book>("settings", {
        book_id: book.id,
        idempotency_key: key.current,
        revision: book.metadata_revision ?? 0,
        name: book.name,
        founder: book.founder ?? "",
        [field]: value,
      });
      key.current = crypto.randomUUID();
      onSaved();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form
      className="book-setup-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (value) void save();
      }}
    >
      {children}
      <Button type="submit" color="secondary" busy={busy} disabled={!value}>
        {action}
      </Button>
      <ErrorMessage message={error} intent="write" />
    </form>
  );
}

export function BookTimezoneSetting({ book, onSaved }: { book: Book; onSaved: () => void }) {
  const [zone, setZone] = useState(book.accounting_timezone || browserTimeZone());
  return (
    <SetupForm book={book} field="accounting_timezone" value={zone} action="Set timezone" onSaved={onSaved}>
      <AccountingTimezone value={zone} onChange={setZone} label="Choose a timezone" />
    </SetupForm>
  );
}

export function BookProfileSetting({ book, onSaved }: { book: Book; onSaved: () => void }) {
  const [profile, setProfile] = useState("");
  return (
    <SetupForm book={book} field="profile" value={profile} action="Set Book type" onSaved={onSaved}>
      <PurposeChoice value={profile} onChange={setProfile} />
    </SetupForm>
  );
}
