import type { ReactNode } from "react";
import { DisplayAmount } from "../components/ui/display-amount";
import "./styles/chart-palette.css";
import "./headline.css";

/** A summary's headline: the one figure that answers it, a caption saying what
 * that figure covers, and the parts beside it. The Dashboard's cards and
 * Activity's summary use it, so a total reads the same everywhere.
 * A string value is an exact amount (DisplayAmount): its cents are smaller.
 * `stacked` puts the parts on their own line, for narrow places. */
export function Headline({
  value,
  caption,
  aside,
  stacked = false,
  className = "",
}: {
  value: ReactNode;
  caption?: ReactNode;
  aside?: ReactNode;
  stacked?: boolean;
  className?: string;
}) {
  return (
    <div className={`vult-headline ${className}`} data-stacked={stacked || undefined}>
      <p>
        <strong className="vult-headline-figure">{typeof value === "string" ? <DisplayAmount value={value} /> : value}</strong>
        {caption && <span className="vult-headline-caption">{caption}</span>}
      </p>
      {aside && <p className="vult-headline-aside">{aside}</p>}
    </div>
  );
}

/** The colour key for income or expenses, matching the charts. */
export function SeriesKey({ series, tight, coloured }: { series: "income" | "expenses"; tight?: boolean; coloured?: boolean }) {
  return <i className="vult-series-key" data-series={series} data-tight={tight || undefined} data-coloured={coloured || undefined} aria-hidden="true" />;
}
