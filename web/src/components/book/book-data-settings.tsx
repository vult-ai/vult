import { ChevronDown, Download01 } from "../../design-system/icons";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { Button as ActionButton } from "../../design-system/catalog/components/vult-ui/button";
import { bookURL, type Book } from "../../lib/api";
import { SettingsRow, SettingsSection } from "../ui/settings-section";

/* The way out: everything in this Book, downloadable at any time, in the same
   files the terminal writes with `vult export`. Nothing here changes the Book. */
export function BookDataSettings({ book }: { book: Book }) {
  return (
    <SettingsSection
      title="Download your data"
      description="Everything in this Book, in open formats, whenever you want it. Your data is never locked in."
    >
      <SettingsRow
        label="Whole Book"
        description="Every entry and account, with the original documents. The same file as vult export --documents."
      >
        <ActionButton href={`/api/v1/${bookURL("exports/book.tar", book.id)}`} size="md" iconLeading={Download01}>
          Download Book
        </ActionButton>
      </SettingsRow>
      <SettingsRow
        label="Entries and accounts"
        description="For spreadsheets and other software."
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
  );
}
