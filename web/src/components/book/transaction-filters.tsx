import { useRef, useState } from "react";
import { FilterLines } from "../../design-system/icons";
import { Dialog, DialogTrigger, Heading, Popover } from "react-aria-components";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { CloseButton } from "../../design-system/catalog/components/vult-ui/close-button";
import { Select } from "../ui/select";
import { names } from "../../lib/api";
import type { BookFilters } from "../../lib/book-state";
import styles from "./transaction-filters.module.css";

/** the shared design system's collection filter composition; values remain host-owned. */
export function TransactionFilters({
  value,
  onChange,
}: {
  value: BookFilters;
  onChange: <K extends keyof BookFilters>(key: K, value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [instant, setInstant] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const count =
    Number(!!value.kind && !["expense", "income"].includes(value.kind));
  const summary = [
    value.kind && !["expense", "income"].includes(value.kind)
      ? names[value.kind]
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      className="contents"
      onPointerDownCapture={() => setInstant(false)}
      onKeyDownCapture={() => setInstant(true)}
    >
      <DialogTrigger isOpen={open} onOpenChange={setOpen}>
        <Button
          ref={trigger}
          size="sm"
          color={count ? "secondary" : "tertiary"}
          iconLeading={FilterLines}
          className={styles.trigger}
          data-filter-count={count}
          aria-label={
            count
              ? `Filter transactions, ${count} active: ${summary}`
              : "Filter transactions"
          }
          title={summary || "Filter transactions"}
        >
          <span className={styles.label}>Filter</span>
          {count ? <span>{count}</span> : null}
        </Button>
        <Popover
          shouldCloseOnInteractOutside={(element) =>
            !trigger.current?.contains(element)
          }
          placement="bottom end"
          offset={8}
          containerPadding={8}
          isNonModal
          data-motion={instant ? "instant" : "spatial"}
          className={`vult-surface-paper w-56 max-w-[calc(100vw-1rem)] overflow-y-auto rounded-xl border border-secondary p-3 shadow-lg outline-hidden ${styles.popover}`}
        >
          <Dialog aria-label="Transaction filters" className="outline-hidden">
            <header className="mb-3 flex items-center justify-between gap-2">
              <Heading
                slot="title"
                className="vult-type-compact-title text-primary"
              >
                Filters
              </Heading>
              <div className="flex items-center gap-1">
                <Button
                  size="sm"
                  color="tertiary"
                  hideLoadingIndicator
                  className={`data-[loading]:opacity-100 ${styles.reset}`}
                  onPress={() => {
                    onChange("kind", "");
                  }}
                >
                  Reset
                </Button>
                <CloseButton
                  label="Close transaction filters"
                  onPress={() => setOpen(false)}
                />
              </div>
            </header>
            <div className="flex flex-col gap-3">
              <Select
                size="sm"
                label="Type"
                aria-label="Filter transaction type"
                value={value.kind}
                onChange={(event) => onChange("kind", event.target.value)}
                options={[
                  { value: "", label: "All types" },
                  ...Object.entries(names).map(([value, label]) => ({
                    value,
                    label,
                  })),
                ]}
              />
            </div>
          </Dialog>
        </Popover>
      </DialogTrigger>
    </div>
  );
}
