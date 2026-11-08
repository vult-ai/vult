import { useEffect, useState, type ReactNode } from "react";
import { parseDate } from "@internationalized/date";
import type { RangeValue } from "@react-types/shared";
import type { DateValue } from "react-aria-components";
import { DateRangePicker } from "../ui/date-range-picker";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { type DashboardRange } from "../../lib/dashboard-period";
import { reportQuickRange } from "../../lib/report-period";
import { ReportDatePicker } from "./report-date-picker";
import { today, type Book } from "../../lib/api";

export function ReportPeriodPicker({
  range,
  position,
  months,
  book,
  onRange,
  disabled,
  triggerLabel,
}: {
  range: DashboardRange;
  position: boolean;
  months?: boolean;
  book: Book;
  onRange: (range: DashboardRange) => void;
  disabled?: boolean;
  triggerLabel?: ReactNode;
}) {
  const calendar = () => ({
    start: parseDate(range.from),
    end: parseDate(range.to),
  });
  const [draft, setDraft] = useState<RangeValue<DateValue> | null>(calendar);
  const [changed, setChanged] = useState(false);
  const reset = () => {
    setDraft(calendar());
    setChanged(false);
  };
  useEffect(reset, [range.from, range.to]);
  const label =
    position && !months
      ? `As of ${formatDate(range.to, "full")}`
      : formatDateRange(range.from, range.to, "full");
  if (position && !months)
    return (
      <ReportDatePicker
        book={book}
        range={range}
        disabled={disabled}
        triggerLabel={triggerLabel}
        onRange={onRange}
      />
    );
  return (
    <DateRangePicker
      aria-label={`Report ${position && !months ? "date" : "period"}: ${label}`}
      value={draft}
      onChange={(value) => {
        setDraft(value);
        setChanged(true);
      }}
      triggerLabel={
        <span className="statement-period-label" title={label}>
          {triggerLabel ?? label}
        </span>
      }
      triggerClassName="statement-period"
      dialogClassName="statement-period-dialog"
      variant="compact"
      visibleMonths={1}
      size="sm"
      isDisabled={disabled}
      onOpenChange={reset}
      onCancel={reset}
      quickRanges={[
        { id: "today", label: "Today", shortLabel: "Today" },
        { id: "month", label: "Month to date", shortLabel: "Month to date" },
        {
          id: "quarter",
          label: "Calendar quarter to date",
          shortLabel: "Calendar QTD",
        },
        {
          id: "calendar_ytd",
          label: "Calendar year to date",
          shortLabel: "Calendar YTD",
        },
        {
          id: "fiscal_ytd",
          label: "Fiscal year to date",
          shortLabel: "Fiscal YTD",
        },
      ]}
      onQuickRangeSelect={(id) =>
        onRange(
          reportQuickRange(
            id,
            book,
            book.accounting_today || today(book.accounting_timezone),
          ),
        )
      }
      footerDescription={
        position
          ? "Each column shows a balance at its ending date."
          : "Includes both dates."
      }
      isApplyDisabled={!draft || !changed || draft.start.compare(draft.end) > 0}
      onApply={(value) => {
        if (value)
          onRange({ from: value.start.toString(), to: value.end.toString() });
      }}
    />
  );
}
