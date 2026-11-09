import { ImageSquare } from "../../design-system/icons";
import { useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { bookLogo, removeBookLogo, saveBookLogo } from "../../lib/book-logo";
import { Button } from "../ui/button";
import { FieldError } from "../ui/error-notice";
import { SettingsSection } from "../ui/settings-section";
import { BookMark } from "../ui/book-mark";

/** The Book's logo, or for a personal Book a photo: shown beside its name in the
 * Book switcher. Owners set it here; agents set it through the same route. */
export function BookLogoSettings({ book, onSaved }: { book: Book; onSaved: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const personal = book.profile === "personal";
  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await work();
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : "The logo could not be saved.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <SettingsSection title={personal ? "Photo" : "Logo"} icon={ImageSquare} description="Shown beside the Book’s name.">
      <div className="book-logo-settings">
        <BookMark book={book} size={64} />
        <div className="book-logo-settings-controls">
          <div className="book-logo-settings-buttons">
            <Button color="secondary" size="sm" busy={busy} onClick={() => input.current?.click()}>
              {bookLogo(book) ? "Replace" : personal ? "Upload photo" : "Upload logo"}
            </Button>
            {bookLogo(book) && (
              <Button color="tertiary" size="sm" disabled={busy} onClick={() => void run(() => removeBookLogo(book))}>
                Remove
              </Button>
            )}
          </div>
          <p className="vult-type-metadata text-tertiary">JPG or PNG, up to 2 MB. Cropped square.</p>
          <FieldError id={`${book.id}-logo-error`} error={error} />
        </div>
        <input
          ref={input}
          hidden
          tabIndex={-1}
          type="file"
          accept="image/jpeg,image/png"
          aria-label={personal ? "Upload Book photo" : "Upload Book logo"}
          onChange={(event) => {
            const file = event.currentTarget.files?.[0];
            event.currentTarget.value = "";
            if (file) void run(() => saveBookLogo(book, file));
          }}
        />
      </div>
    </SettingsSection>
  );
}
