import type { FormEvent, ReactNode } from "react";
import { ErrorMessage } from "../form";
import { Button } from "./button";
import { Dialog, useDialogPending } from "./dialog";
import "./confirm-action.css";

const irreversibleLine = "You can’t undo this.";

/**
 * The one row of buttons for confirming a consequential step: Cancel, then
 * the action named as verb and object. The confirm label never changes while
 * pending; the button shows progress itself.
 */
export function ConfirmFooter({
  confirmLabel,
  tone = "destructive",
  busy = false,
  disabled = false,
  submit = false,
  onCancel,
  onConfirm,
}: {
  confirmLabel: string;
  tone?: "destructive" | "default";
  busy?: boolean;
  disabled?: boolean;
  /** The confirm button submits its enclosing form instead of calling onConfirm. */
  submit?: boolean;
  onCancel: () => void;
  onConfirm?: () => void;
}) {
  return (
    <div className="vult-confirm-actions">
      <Button color="secondary" disabled={busy} onClick={onCancel}>
        Cancel
      </Button>
      <Button
        color={tone === "destructive" ? "destructive" : "primary"}
        type={submit ? "submit" : "button"}
        busy={busy}
        disabled={disabled}
        onClick={submit ? undefined : onConfirm}
      >
        {confirmLabel}
      </Button>
    </div>
  );
}

/** One sentence on what happens, with the standard line when it is permanent. */
export function ConfirmDetail({
  children,
  irreversible = false,
}: {
  children: ReactNode;
  irreversible?: boolean;
}) {
  return (
    <p className="vult-confirm-detail vult-type-ui">
      {children}
      {irreversible ? <> {irreversibleLine}</> : null}
    </p>
  );
}

/**
 * Confirmation for destructive or irreversible steps. The title asks the
 * question ("Delete this file?"); the detail says what happens and what stays.
 * Escape, the close button and Cancel are unavailable while the step runs.
 */
export function ConfirmAction({
  open,
  title,
  detail,
  irreversible = false,
  confirmLabel,
  tone = "destructive",
  busy = false,
  error,
  confirmDisabled = false,
  onConfirm,
  onCancel,
  returnTo,
  className = "",
  children,
}: {
  open: boolean;
  title: string;
  detail: ReactNode;
  irreversible?: boolean;
  confirmLabel: string;
  tone?: "destructive" | "default";
  busy?: boolean;
  error?: unknown;
  confirmDisabled?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  returnTo?: HTMLElement | null;
  className?: string;
  /** Specific evidence the decision needs, such as the Books that lose access. */
  children?: ReactNode;
}) {
  return (
    <Dialog
      open={open}
      title={title}
      description={null}
      returnTo={returnTo}
      size="small"
      className={`vult-confirm ${className}`.trim()}
      onOpenChange={(next) => {
        if (!next && !busy) onCancel();
      }}
    >
      <ConfirmBody busy={busy}>
        <ConfirmDetail irreversible={irreversible}>{detail}</ConfirmDetail>
        {children}
        <ErrorMessage message={error} intent="write" />
        <ConfirmFooter
          confirmLabel={confirmLabel}
          tone={tone}
          busy={busy}
          disabled={confirmDisabled}
          onCancel={onCancel}
          onConfirm={onConfirm}
        />
      </ConfirmBody>
    </Dialog>
  );
}

function ConfirmBody({ busy, children }: { busy: boolean; children: ReactNode }) {
  useDialogPending(busy);
  return (
    <form
      className="vult-confirm-body"
      onSubmit={(event: FormEvent) => event.preventDefault()}
    >
      {children}
    </form>
  );
}
