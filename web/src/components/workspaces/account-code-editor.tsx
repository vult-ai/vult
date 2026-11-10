import { useEffect, useRef, useState } from "react";
import {
  readAccountCode,
  saveAccountCode,
  type AccountCodeView,
} from "../../lib/account-codes";
import { focusWithoutScroll } from "../../design-system/focus-without-scroll";
import { formatDateTime } from "../../lib/date-format";
import { Field, ErrorMessage } from "../form";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { LoadingState } from "../ui/loading-state";

export function AccountCodeEditor({
  book,
  account,
  open: controlledOpen,
  onOpenChange,
  returnTo,
}: {
  book: string;
  account: { code: string; name: string; gl_code?: string };
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  returnTo?: HTMLElement | null;
}) {
  const [localOpen, setLocalOpen] = useState(false);
  const open = controlledOpen ?? localOpen;
  const setOpen = onOpenChange ?? setLocalOpen;
  const [loaded, setLoaded] = useState<AccountCodeView>();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [reload, setReload] = useState(0);
  const codeInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (open && loaded && codeInput.current)
      focusWithoutScroll(codeInput.current);
  }, [open, loaded]);
  const trigger = useRef<HTMLButtonElement>(null),
    key = useRef(crypto.randomUUID()),
    generation = useRef(0);
  const identity = useRef(`${book}:${account.code}`);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const seq = ++generation.current;
    setLoaded(undefined);
    setError("");
    key.current = crypto.randomUUID();
    readAccountCode(book, account.code, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted && seq === generation.current) {
          setLoaded(value);
          setDraft(value.gl_code);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted)
          setError(
            e instanceof Error ? e.message : "Account code could not be read.",
          );
      });
    return () => {
      controller.abort();
      generation.current++;
    };
  }, [open, book, account.code, reload]);
  useEffect(() => {
    const next = `${book}:${account.code}`;
    if (identity.current !== next) {
      identity.current = next;
      setOpen(false);
      setPending(false);
    }
  }, [book, account.code]);
  return (
    <>
      {controlledOpen === undefined && (
        <Button
          size="sm"
          color="tertiary"
          onClick={(event) => {
            trigger.current = event.currentTarget;
            setOpen(true);
          }}
        >
          Edit GL code
        </Button>
      )}
      <Dialog
        open={open}
        title="GL code"
        className="account-code-dialog"
        description={`${account.name} · This number is shared by all entries using this account. Changing it does not reclassify transactions.`}
        returnTo={returnTo ?? trigger.current}
        onOpenChange={(value) => {
          if (!pending) setOpen(value);
        }}
      >
        <form
          className="dialog-form"
          onSubmit={async (event) => {
            event.preventDefault();
            if (!loaded || pending) return;
            const seq = generation.current;
            setPending(true);
            setError("");
            try {
              await saveAccountCode(
                book,
                account.code,
                draft,
                loaded.revision,
                key.current,
              );
              if (seq === generation.current) setOpen(false);
            } catch (e) {
              if (seq === generation.current)
                setError(
                  e instanceof Error
                    ? e.message
                    : "Account code could not be saved.",
                );
            } finally {
              if (seq === generation.current) setPending(false);
            }
          }}
        >
          <Field
            label="GL code"
            hint="Use the number from the chart of accounts. Leading zeros are kept."
          >
            <input
              ref={codeInput}
              name="gl_code"
              autoComplete="off"
              value={draft}
              maxLength={32}
              disabled={!loaded || pending}
              onChange={(e) => {
                setDraft(e.currentTarget.value);
                key.current = crypto.randomUUID();
              }}
            />
          </Field>
          <details>
            <summary>Account reference</summary>
            {/* A fixed value is shown as text, never as a field. */}
            <code className="vult-code" aria-label="Stable account reference">
              {account.code}
            </code>
          </details>
          {!loaded && !error && <LoadingState scope="inline" label="Loading account code…" />}
          <ErrorMessage message={error} />
          {error && (
            <Button
              size="sm"
              color="tertiary"
              disabled={pending}
              onClick={() => setReload((value) => value + 1)}
            >
              Refresh account code
            </Button>
          )}
          {!!loaded?.history.length && (
            <details>
              <summary>
                {loaded.history.length === 100
                  ? "Latest 100 code changes"
                  : "Code history"}
              </summary>
              <ul>
                {loaded.history.map((item) => (
                  <li key={item.revision}>
                    {item.prior_gl_code ? (
                      <span className="vult-code">{item.prior_gl_code}</span>
                    ) : (
                      "No code"
                    )}{" "}
                    →{" "}
                    {item.gl_code ? (
                      <span className="vult-code">{item.gl_code}</span>
                    ) : (
                      "No code"
                    )}{" "}
                    · {formatDateTime(item.recorded_at)} · {item.actor}
                  </li>
                ))}
              </ul>
            </details>
          )}
          <div className="dialog-footer">
            <Button
              type="submit"
              disabled={!loaded || draft.trim() === loaded.gl_code}
              busy={pending}
            >
              Save GL code
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
