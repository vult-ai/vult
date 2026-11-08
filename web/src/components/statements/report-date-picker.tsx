import { useState, type ReactNode } from "react";
import { parseDate, type CalendarDate } from "@internationalized/date";
import {
  Calendar as CalendarIcon,
  ChevronLeft,
  ChevronRight,
} from "../../design-system/icons";
import {
  Calendar,
  CalendarGrid,
  CalendarGridBody,
  CalendarGridHeader,
  CalendarHeaderCell,
  DatePicker,
  Dialog,
  Group,
  Heading,
  Popover,
} from "react-aria-components";
import { Button } from "../../design-system/catalog/components/base/buttons/button";
import { InputDateBase } from "../../design-system/catalog/components/base/input/input-date";
import { CalendarCell } from "../../design-system/catalog/components/application/date-picker/cell";
import { formatDate } from "../../lib/date-format";
import {
  reportComparisonDate,
  reportFiscalStart,
} from "../../lib/report-period";
import { today, type Book } from "../../lib/api";
import type { DashboardRange } from "../../lib/dashboard-period";

export function ReportDatePicker({
  range,
  book,
  onRange,
  disabled,
  triggerLabel,
}: {
  range: DashboardRange;
  book: Book;
  onRange: (range: DashboardRange) => void;
  disabled?: boolean;
  triggerLabel?: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [date, setDate] = useState<CalendarDate | null>(parseDate(range.to));
  const label = `As of ${formatDate(range.to, "full")}`;
  const apply = (value: CalendarDate) => {
    const to = value.toString();
    onRange({
      from: range.from > to ? reportFiscalStart(book, to) : range.from,
      to,
    });
    setOpen(false);
  };
  const current = book.accounting_today || today(book.accounting_timezone);
  return (
    <DatePicker
      aria-label={`Report date: ${label}`}
      value={date}
      onChange={setDate}
      isOpen={open}
      onOpenChange={(next) => {
        if (next) setDate(parseDate(range.to));
        setOpen(next);
      }}
      shouldCloseOnSelect={false}
      isDisabled={disabled}
    >
      <Group>
        <Button
          size="sm"
          color="secondary"
          iconLeading={CalendarIcon}
          className="statement-period"
        >
          <span className="statement-period-label" title={label}>
            {triggerLabel ?? label}
          </span>
        </Button>
      </Group>
      <Popover
        placement="bottom start"
        containerPadding={12}
        offset={12}
        className="vult-dropdown-popover"
      >
        <Dialog
          aria-label="Report as-of date"
          className="statement-date-dialog"
          data-vult-surface="overlay"
        >
          <div
            className="statement-date-presets"
            role="group"
            aria-label="Common position dates"
          >
            <Button
              size="sm"
              color="tertiary"
              slot={null}
              onClick={() => apply(parseDate(current))}
            >
              Today
            </Button>
            <Button
              size="sm"
              color="tertiary"
              slot={null}
              onClick={() =>
                apply(
                  parseDate(reportComparisonDate("month_end", book, current)),
                )
              }
            >
              Last month end
            </Button>
            <Button
              size="sm"
              color="tertiary"
              slot={null}
              onClick={() =>
                apply(
                  parseDate(
                    reportComparisonDate("fiscal_year_end", book, current),
                  ),
                )
              }
            >
              Last fiscal year end
            </Button>
          </div>
          <Calendar aria-label="Position date" value={date} onChange={setDate}>
            <header className="statement-date-heading">
              <Button
                slot="previous"
                size="sm"
                color="tertiary"
                aria-label="Previous month"
                iconLeading={ChevronLeft}
              />
              <Heading />
              <Button
                slot="next"
                size="sm"
                color="tertiary"
                aria-label="Next month"
                iconLeading={ChevronRight}
              />
            </header>
            <CalendarGrid weekdayStyle="short">
              <CalendarGridHeader>
                {(day) => <CalendarHeaderCell>{day}</CalendarHeaderCell>}
              </CalendarGridHeader>
              <CalendarGridBody>
                {(day) => <CalendarCell date={day} variant="compact" />}
              </CalendarGridBody>
            </CalendarGrid>
          </Calendar>
          <p className="statement-caption">
            Balances include the opening position and postings through this
            date.
          </p>
          <InputDateBase size="sm" aria-label="As-of date" />
          <div className="statement-date-actions">
            <Button
              size="sm"
              color="tertiary"
              slot={null}
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              slot={null}
              isDisabled={!date || date.toString() === range.to}
              onClick={() => {
                if (date) apply(date);
              }}
            >
              Apply
            </Button>
          </div>
        </Dialog>
      </Popover>
    </DatePicker>
  );
}
