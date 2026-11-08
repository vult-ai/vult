import { useRef, useState } from "react";
import { ChevronDown, Plus, Settings01 } from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import type { Book } from "../../lib/api";
import { BookMark } from "../ui/book-mark";

export function BookSwitcher({
  books,
  selected,
  locked,
  onSelect,
  onManage,
}: {
  books: Book[];
  selected: string;
  locked: boolean;
  onSelect: (id: string) => void;
  onManage?: () => void;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const current = books.find((book) => book.id === selected);
  const name = current?.name || "Select Book";
  return (
    <div className="book-switcher" aria-label="Book selection">
      <Dropdown.Root
        isOpen={open && !locked}
        onOpenChange={(next) => {
          if (!locked || !next) setOpen(next);
        }}
      >
        <Button
          ref={trigger}
          color="tertiary"
          size="sm"
          isDisabled={locked}
          aria-label={`Switch Book, current Book ${name}`}
          title={locked ? "Close the current form before changing Books" : name}
          className="book-switcher-trigger vult-focus-contained w-auto max-w-full min-w-0 justify-start overflow-hidden text-left *:data-text:min-w-0 *:data-text:overflow-hidden"
          iconTrailing={(props) => (
            <ChevronDown {...props} className="in-aria-expanded:-rotate-180" />
          )}
        >
          <span className="book-switcher-current">
            {current && <BookMark book={current} />}
            <span className="vult-type-label block min-w-0 truncate text-secondary">
              {name}
            </span>
          </span>
        </Button>
        <Dropdown.Popover
          placement="bottom left"
        >
          <Dropdown.Menu
            aria-label="Books"
            selectionMode="none"
            disabledKeys={
              locked ? books.map((book) => book.id) : []
            }
          >
            <Dropdown.Section
              aria-label="Existing Books"
              selectionMode="single"
              disallowEmptySelection
              selectedKeys={[selected]}
              onSelectionChange={(keys) => {
                if (locked || keys === "all") return;
                const id = String([...keys][0] ?? "");
                if (id !== selected && books.some((book) => book.id === id))
                  onSelect(id);
              }}
            >
              {books.map((book) => (
                <Dropdown.Item key={book.id} id={book.id} label={book.name} icon={() => <BookMark book={book} />}>
                  {book.name}
                </Dropdown.Item>
              ))}
            </Dropdown.Section>
            {onManage && <Dropdown.Item id="manage-books" icon={Settings01} selectionIndicator="none" onAction={()=>{setOpen(false);onManage();}}>Manage Books</Dropdown.Item>}
          </Dropdown.Menu>
        </Dropdown.Popover>
      </Dropdown.Root>
    </div>
  );
}
