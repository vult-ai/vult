import "./v0.css";
import { useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { openCreatedBook } from "../../lib/open-created-book";
import { CreateBook } from "../onboarding/welcome";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { PageToolbar, Surface } from "../workspaces/workspace-frame";

export function V0Books({
  books,
  selected,
  onSelect,
}: {
  books: Book[];
  selected: string;
  onSelect: (id: string) => void;
}) {
  const [creating, setCreating] = useState(false);
  const trigger = useRef<HTMLElement | null>(null);
  return (
    <div className="workspace-page">
      <PageToolbar
        actions={
          <Button
            size="sm"
            onClick={(e) => {
              trigger.current = e.currentTarget;
              setCreating(true);
            }}
          >
            Create Book
          </Button>
        }
      />
      <Surface title="Your Books">
        <div className="v0-record-list">
          {books.map((b) => (
            <div className="v0-record-row" key={b.id}>
              <div>
                <strong>{b.name}</strong>
                <span>
                  {b.profile === "personal"
                    ? "Personal"
                    : b.profile === "business"
                      ? "Business"
                      : "Profile not set"}{" "}
                  · {b.currency}
                </span>
              </div>
              <Button
                size="sm"
                color="tertiary"
                onClick={() => onSelect(b.id)}
                disabled={b.id === selected}
              >
                {b.id === selected ? "Current Book" : "Open Book"}
              </Button>
            </div>
          ))}
        </div>
      </Surface>
      <Dialog
        title="Create a Book"
        description="Each Book keeps its own accounts, entries and sources."
        open={creating}
        onOpenChange={setCreating}
        returnTo={trigger.current}
      >
        {creating && (
          <div className="create-book-dialog">
            <CreateBook onCreated={(book) => void openCreatedBook(book)} />
          </div>
        )}
      </Dialog>
    </div>
  );
}
