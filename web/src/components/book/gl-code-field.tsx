import { useRef, useState } from "react";
import type { State } from "../../lib/api";
import { NameLink } from "../ui/name-link";
import { AccountCodeEditor } from "../workspaces/account-code-editor";

/** The posted account owns the number; editing it never reclassifies an entry. */
export function GLCodeField({
  book,
  account,
  value,
  title,
  disabled,
  wrap = false,
}: {
  book: string;
  account?: State["accounts"][number];
  value: string;
  title?: string;
  disabled?: boolean;
  /** Wraps a long name, where there is room for every word (details panels). */
  wrap?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  if (!account) return <span title={title}>{value}</span>;
  const code = account.gl_code?.trim();
  const prefix = code ? `${code} · ` : "";
  return (
    <>
      <NameLink
        ref={trigger}
        name={code && value.startsWith(prefix) ? value.slice(prefix.length) : value}
        leading={code ? <span className="vult-code whitespace-pre">{prefix}</span> : undefined}
        title={title}
        aria-label={`${account.gl_code ? "Edit" : "Set"} GL code for ${account.name}`}
        className="max-w-full min-w-0 gap-0 text-secondary"
        nameClassName={wrap ? "gl-code-field-wrap" : "truncate"}
        disabled={disabled}
        onClick={() => setOpen(true)}
      />
      <AccountCodeEditor
        book={book}
        account={account}
        open={open}
        onOpenChange={setOpen}
        returnTo={trigger.current}
      />
    </>
  );
}
