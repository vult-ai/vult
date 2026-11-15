import "./v0.css";
import { useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { openCreatedBook } from "../../lib/open-created-book";
import { CreateBook } from "../onboarding/welcome";
import { archiveBook } from "../book/book-data-settings";
import { ErrorNotice } from "../ui/error-notice";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { PageToolbar, Surface } from "../workspaces/workspace-frame";

export function V0Books({
  books,
  selected,
  onSelect,
  onChanged = () => undefined,
}: {
  books: Book[];
  selected: string;
  onSelect: (id: string) => void;
  /** Called after a Book is archived or brought back. */
  onChanged?: () => void;
}) {
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const [problem, setProblem] = useState<unknown>(null);
  // Archived Books come last, each with a way back.
  const ordered = [...books.filter((b) => !b.archived), ...books.filter((b) => b.archived)];
  const archive = async (b: Book) => {
    setBusy(b.id);
    setProblem(null);
    try {
      await archiveBook(b.id, !b.archived);
      onChanged();
    } catch (e) {
      setProblem(e);
    } finally {
      setBusy("");
    }
  };
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
      <Surface title="Books">
        <div className="v0-record-list">
          {ordered.map((b) => (
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
                  {b.archived ? " · Archived" : ""}
                </span>
              </div>
              <span className="v0-record-actions">
                <Button size="sm" color="tertiary" busy={busy === b.id} disabled={busy !== ""} onClick={() => void archive(b)}>
                  {b.archived ? "Unarchive" : "Archive"}
                </Button>
                <Button
                  size="sm"
                  color="tertiary"
                  onClick={() => onSelect(b.id)}
                  disabled={b.id === selected}
                >
                  {b.id === selected ? "Current Book" : "Open Book"}
                </Button>
              </span>
            </div>
          ))}
        </div>
        <ErrorNotice error={problem} intent="write" />
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
