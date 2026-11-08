import { useEffect, useRef, useState } from "react";
import { Copy01, Check } from "../../design-system/icons";
import type { Book } from "../../lib/api";
import type { DashboardRange } from "../../lib/dashboard-period";
import { currentViewURL } from "../../lib/dashboard-location";
import { Dialog } from "../ui/dialog";
import { Field } from "../form";
import { Button } from "../ui/button";
import { announce } from "../../lib/announce";

export function CopyCurrentView({
  book,
  range,
  disabled = false,
  href,
}: {
  book: Book;
  range: DashboardRange;
  disabled?: boolean;
  href?: string;
}) {
  const generation = useRef(0);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const [copied, setCopied] = useState(false);
  const [manual, setManual] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(
    () => () => {
      generation.current++;
      clearTimeout(timer.current);
    },
    [],
  );
  useEffect(() => {
    generation.current++;
    setCopied(false);
    setManual("");
  }, [book.id, range.from, range.to, href]);
  const copy = () => {
    const seq = ++generation.current;
    const text = href ?? currentViewURL(book, range);
    // Start clipboard access within the gesture. Copy never holds an action
    // spinner or blocks the selected report while the browser grants access.
    const done = () => {
      if (seq !== generation.current) return;
      setCopied(true);
      announce("View link copied.");
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1800);
    };
    if (!navigator.clipboard?.writeText) {
      setManual(text);
      return;
    }
    try {
      void navigator.clipboard.writeText(text).then(done, () => {
        if (seq === generation.current) setManual(text);
      });
    } catch {
      if (seq === generation.current) setManual(text);
    }
  };
  const Icon = copied ? Check : Copy01;
  return (
    <>
      <Button
        color="tertiary"
        size="sm"
        disabled={disabled}
        onClick={(event) => {
          trigger.current = event.currentTarget;
          copy();
        }}
        aria-label={copied ? "Current view copied" : "Copy current view"}
      >
        <Icon className="size-4" aria-hidden />
        <span className="hidden xl:inline">
          {copied ? "Copied" : "Copy current view"}
        </span>
      </Button>
      <Dialog
        title="Copy current view"
        open={!!manual}
        onOpenChange={(open) => {
          if (!open) setManual("");
        }}
        returnTo={trigger.current}
      >
        <Field label="Copy this link">
          <input
            autoFocus
            readOnly
            value={manual}
            onFocus={(e) => e.currentTarget.select()}
          />
        </Field>
      </Dialog>
    </>
  );
}
