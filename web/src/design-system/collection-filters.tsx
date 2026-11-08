import { useLayoutEffect, useRef } from "react";
import { Button } from "./catalog/components/vult-ui/button";
import { SeriesKey } from "./headline";

export type CollectionFilter = {
  id: string;
  label: string;
  count: number;
  /** Money in or out, keyed by position like the charts, never by colour. */
  series?: "income" | "expenses";
};

/** the shared design system's counted collection filters. The host owns the view and its counts. */
export function CollectionFilters({
  items,
  value,
  onChange,
  disabled,
  "aria-label": ariaLabel,
}: {
  items: readonly CollectionFilter[];
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
  "aria-label": string;
}) {
  const group = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    group.current
      ?.querySelector<HTMLElement>('button[aria-pressed="true"]')
      ?.scrollIntoView?.({
        block: "nearest",
        inline: "nearest",
        behavior: "instant",
      });
  }, [value]);
  return (
    <div
      ref={group}
      className="flex shrink-0 items-center gap-1"
      role="group"
      aria-label={ariaLabel}
      data-vult-region="collection-filters"
      onFocusCapture={(event) => {
        if (event.target instanceof HTMLElement) {
          event.target.scrollIntoView?.({
            block: "nearest",
            inline: "nearest",
            behavior: "instant",
          });
        }
      }}
    >
      {items.map((item) => (
        <Button
          key={item.id}
          size="sm"
          color={value === item.id ? "secondary" : "tertiary"}
          aria-pressed={value === item.id}
          isDisabled={disabled}
          title={item.label}
          onPress={() => onChange(item.id)}
        >
          <span className="flex min-w-0 items-center gap-1.5">
            {item.series && <SeriesKey series={item.series} tight coloured />}
            <span className="truncate">{item.label}</span>
            <span
              data-collection-count
              className={`vult-type-metadata shrink-0 rounded px-1 text-secondary tabular-nums ${value === item.id ? "bg-secondary" : ""}`}
            >
              {item.count.toLocaleString()}
            </span>
          </span>
        </Button>
      ))}
    </div>
  );
}
