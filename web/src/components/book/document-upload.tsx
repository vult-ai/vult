import "./document-upload.css";
import { useCallback, useId, useRef, useState, type DragEvent, type ReactNode } from "react";
import { AlertCircle, CheckCircle, UploadCloud02 } from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { uploadDocument, uploadType, UPLOAD_ACCEPT, type BookDocument } from "../../lib/documents";
import { presentFailure } from "../../lib/errors";

type Item = { id: string; name: string; state: "adding" | "added" | "kept" | "failed"; detail?: string };

/** The owner adds originals to the Book. Each file is checked here first, then
 * added one at a time; the list says what happened to each. */
export function useDocumentUpload(bookId: string, onAdded: (doc: BookDocument) => void) {
  const [items, setItems] = useState<Item[]>([]);
  const queue = useRef(Promise.resolve());
  const add = useCallback((files: FileList | File[]) => {
    for (const file of Array.from(files)) {
      const id = crypto.randomUUID();
      const checked = uploadType(file);
      if ("error" in checked) {
        setItems((list) => [{ id, name: file.name, state: "failed" as const, detail: checked.error }, ...list].slice(0, 50));
        continue;
      }
      setItems((list) => [{ id, name: file.name, state: "adding" as const }, ...list].slice(0, 50));
      const set = (patch: Partial<Item>) => setItems((list) => list.map((x) => (x.id === id ? { ...x, ...patch } : x)));
      queue.current = queue.current.then(() =>
        uploadDocument(bookId, file, id)
          .then((result) => {
            set({ state: result.reused ? "kept" : "added", detail: result.reused ? "Already in this Book" : undefined });
            onAdded(result.document);
          })
          .catch((error) => set({ state: "failed", detail: presentFailure(error, { intent: "write", resource: "sources" }).message })),
      );
    }
  }, [bookId, onAdded]);
  return { items, add, clear: () => setItems((list) => list.filter((x) => x.state === "adding")) };
}

/** Accepts files dropped anywhere on its children, with a visible target while dragging. */
export function DropArea({ onFiles, label, children, className = "", hidden }: { onFiles: (files: FileList) => void; label: string; children: ReactNode; className?: string; hidden?: boolean }) {
  const [over, setOver] = useState(false);
  const depth = useRef(0);
  const files = (e: DragEvent) => [...e.dataTransfer.types].includes("Files");
  return (
    <div
      className={`document-drop-area ${className}`}
      hidden={hidden}
      data-dragging={over || undefined}
      onDragEnter={(e) => { if (!files(e)) return; e.preventDefault(); depth.current++; setOver(true); }}
      onDragOver={(e) => { if (files(e)) { e.preventDefault(); e.dataTransfer.dropEffect = "copy"; } }}
      onDragLeave={() => { depth.current = Math.max(0, depth.current - 1); if (!depth.current) setOver(false); }}
      onDrop={(e) => { if (!files(e)) return; e.preventDefault(); depth.current = 0; setOver(false); onFiles(e.dataTransfer.files); }}
    >
      {children}
      {over && <div className="document-drop-overlay vult-type-label" aria-hidden="true"><UploadCloud02 className="size-5" />{label}</div>}
    </div>
  );
}

/** Opens the system file chooser. */
export function ChooseFiles({ onFiles, children = "Choose files", color = "secondary" }: { onFiles: (files: FileList) => void; children?: ReactNode; color?: "primary" | "secondary" }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <input ref={input} type="file" multiple accept={UPLOAD_ACCEPT} hidden aria-hidden="true" tabIndex={-1}
        onChange={(e) => { if (e.currentTarget.files?.length) onFiles(e.currentTarget.files); e.currentTarget.value = ""; }} />
      <Button size="sm" color={color} iconLeading={UploadCloud02} onClick={() => input.current?.click()}>{children}</Button>
    </>
  );
}

/** The side panel while nothing is selected: drop or choose files, and what happened to each. */
export function UploadPanel({ bookName, items, onFiles, onClear }: { bookName: string; items: Item[]; onFiles: (files: FileList) => void; onClear: () => void }) {
  const heading = useId();
  const done = items.some((x) => x.state !== "adding");
  return (
    <div className="document-upload-panel">
      <DropArea onFiles={onFiles} label={`Drop to add to ${bookName}`} className="document-drop-zone">
        <div className="document-drop-zone-body">
          <UploadCloud02 className="size-5 text-tertiary" aria-hidden />
          <p className="vult-type-label text-primary">Drop files here</p>
          <p className="vult-type-metadata text-tertiary">PDF, CSV, PNG, JPEG or WebP · Up to 32 MiB each</p>
          <ChooseFiles onFiles={onFiles} />
        </div>
      </DropArea>
      {items.length > 0 && (
        <section aria-labelledby={heading} className="document-upload-list">
          <header>
            <h3 id={heading} className="vult-type-label">Added just now</h3>
            {done && <button type="button" className="document-upload-clear vult-type-metadata vult-focus-target" onClick={onClear}>Clear</button>}
          </header>
          <ul role="status" aria-live="polite">
            {items.map((x) => (
              <li key={x.id} data-state={x.state}>
                {x.state === "failed" ? <AlertCircle className="size-4" aria-hidden /> : x.state === "adding" ? <span className="document-upload-spinner" aria-hidden /> : <CheckCircle className="size-4" aria-hidden />}
                <span className="document-upload-name vult-type-ui" title={x.name}>{x.name}</span>
                <span className="document-upload-state vult-type-metadata">{x.state === "adding" ? "Adding…" : x.state === "added" ? "Added" : x.detail}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
