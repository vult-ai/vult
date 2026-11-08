import "./v0.css";
import type { Book } from "../../lib/api";
import { Button } from "../ui/button";
import { PageToolbar, Surface } from "../workspaces/workspace-frame";
export function V0Books({
  books,
  selected,
  onSelect,
  onCreate,
}: {
  books: Book[];
  selected: string;
  onSelect: (id: string) => void;
  onCreate: (trigger: HTMLElement) => void;
}) {
  return (
    <div className="workspace-page">
      <PageToolbar
        actions={
          <Button size="sm" onClick={(e) => onCreate(e.currentTarget)}>
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
    </div>
  );
}
