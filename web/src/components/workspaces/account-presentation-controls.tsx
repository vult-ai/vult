import { useEffect, useRef, useState } from "react";
import { Settings01 } from "../../design-system/icons";
import { Button as IconButton } from "../../design-system/catalog/components/vult-ui/button";
import { Dialog } from "../ui/dialog";
import { Button } from "../ui/button";
import { Select } from "../ui/select";
import { Field, ErrorMessage } from "../form";
import {
  accountPresentationKey,
  emptyAccountPresentation,
  readAccountPresentation,
  saveAccountPresentation,
  type AccountPresentation,
  type BookPresentation,
} from "../../lib/account-presentation";
import { useInstitutionCatalog } from "../../lib/use-institution-catalog";
import { LoadingState } from "../ui/loading-state";
import { Notice } from "../ui/notice";
export { useInstitutionCatalog };

function snapshot(bookId: string) {
  try {
    return { bookId, value: readAccountPresentation(bookId), error: "" };
  } catch {
    return {
      bookId,
      value: emptyAccountPresentation(),
      error: "Saved display preferences could not be read in this browser.",
    };
  }
}
export function useAccountPresentation(bookId: string) {
  const [state, setState] = useState(() => snapshot(bookId));
  const activeBook = useRef(bookId);
  activeBook.current = bookId;
  useEffect(() => {
    const refresh = () => setState(snapshot(bookId));
    const storage = (event: StorageEvent) => {
      if (event.key === null || event.key === accountPresentationKey(bookId))
        refresh();
    };
    const local = (event: Event) => {
      if ((event as CustomEvent).detail === bookId) refresh();
    };
    refresh();
    window.addEventListener("storage", storage);
    window.addEventListener("vult-account-presentation", local);
    return () => {
      window.removeEventListener("storage", storage);
      window.removeEventListener("vult-account-presentation", local);
    };
  }, [bookId]);
  const current = state.bookId === bookId ? state : snapshot(bookId);
  const update = (change: (value: BookPresentation) => BookPresentation) => {
    if (activeBook.current !== bookId)
      throw new Error(
        "The selected Book changed. Reopen account customization.",
      );
    try {
      const next = change(readAccountPresentation(bookId));
      saveAccountPresentation(bookId, next);
      setState({ bookId, value: next, error: "" });
    } catch (error) {
      if (error instanceof Error && error.message.includes("Book changed"))
        throw error;
      throw new Error(
        "Display preferences could not be saved in this browser. The ledger is unchanged.",
      );
    }
  };
  return { ...current, update };
}
export function AccountPresentationButton({
  name,
  onEdit,
}: {
  name: string;
  onEdit: (trigger: HTMLElement) => void;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  return (
    <div className="account-presentation-edit" title="Customize account">
      <IconButton
        ref={trigger}
        size="sm"
        color="tertiary"
        iconLeading={Settings01}
        aria-label={`Customize account ${name}`}
        aria-haspopup="dialog"
        onPress={() => {
          if (trigger.current) onEdit(trigger.current);
        }}
      />
    </div>
  );
}
export function AccountPresentationDialog({
  account,
  initial,
  catalog,
  onSave,
  onClose,
  returnTo,
}: {
  account: { code: string; name: string; currency: string } | null;
  initial: AccountPresentation;
  catalog: ReturnType<typeof useInstitutionCatalog>;
  onSave: (value: AccountPresentation) => void;
  onClose: () => void;
  returnTo: HTMLElement | null;
}) {
  const lastReturnTo = useRef(returnTo);
  if (account && returnTo) lastReturnTo.current = returnTo;
  return (
    <Dialog
      title="Customize account"
      description="Set a purpose name and choose an institution logo. Saved for this Book in this browser."
      open={!!account}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      returnTo={lastReturnTo.current}
    >
      {account && (
        <AccountPresentationForm
          key={account.code}
          account={account}
          initial={initial}
          catalog={catalog}
          onSave={onSave}
        />
      )}
    </Dialog>
  );
}
function AccountPresentationForm({
  account,
  initial,
  catalog,
  onSave,
}: {
  account: { code: string; name: string; currency: string };
  initial: AccountPresentation;
  catalog: ReturnType<typeof useInstitutionCatalog>;
  onSave: (value: AccountPresentation) => void;
}) {
  const [alias, setAlias] = useState(initial.alias);
  const [institutionId, setInstitution] = useState(initial.institutionId);
  const [error, setError] = useState("");
  const options = [
    { value: "", label: "No institution logo" },
    ...catalog.institutions.map((item) => ({
      value: item.id,
      label: item.name,
    })),
  ];
  if (institutionId && !options.some((item) => item.value === institutionId))
    options.push({ value: institutionId, label: "Saved institution" });
  return (
    <form
      className="dialog-form"
      onSubmit={(event) => {
        event.preventDefault();
        setError("");
        try {
          onSave({ alias: alias.trim(), institutionId });
        } catch (error) {
          setError(
            error instanceof Error
              ? error.message
              : "Display preferences could not be saved.",
          );
        }
      }}
    >
      <Field label="Original ledger name">
        <input value={account.name} readOnly />
      </Field>
      <Field
        label="Purpose name"
        hint="Leave blank to show the original account name."
      >
        <input
          name="alias"
          value={alias}
          maxLength={80}
          placeholder="Operating, Payroll, Tax reserve…"
          onChange={(event) => setAlias(event.currentTarget.value)}
        />
      </Field>
      {(account.currency || initial.institutionId) && (
        <>
          <Select
            label="Institution"
            aria-label="Institution"
            value={institutionId}
            onChange={(event) => setInstitution(event.currentTarget.value)}
            disabled={catalog.status !== "ready"}
            options={options}
            hint="Choose the institution associated with this account. All accounts reuse its shared logo."
          />
          {catalog.status === "loading" && (
            <LoadingState scope="inline" label="Loading institution logos…" />
          )}
          {catalog.status === "unconfigured" && (
            <Notice tone="info">
              Institution logos aren’t set up on this installation.
            </Notice>
          )}
          {catalog.status === "error" && (
            <ErrorMessage
              message="Institution logos are unavailable."
              intent="read"
              resource="accounts"
              onRetry={catalog.retry}
            />
          )}
        </>
      )}
      <ErrorMessage message={error} />
      <div className="dialog-footer">
        <Button type="submit">Save changes</Button>
      </div>
    </form>
  );
}
