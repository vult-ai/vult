import { useContext, useRef, useState } from "react";
import { getLocalTimeZone, parseDate, today as localToday, type CalendarDate } from "@internationalized/date";
import type { RangeValue } from "@react-types/shared";
import { Calendar as CalendarIcon, ChevronDown, ChevronLeft, ChevronRight, ChevronUp } from "../../design-system/icons";
import {
  CalendarCell,
  CalendarGrid,
  CalendarGridBody,
  CalendarGridHeader,
  CalendarHeaderCell,
  Dialog,
  DialogTrigger,
  Popover,
  RangeCalendar,
  RangeCalendarStateContext,
  type DateValue,
} from "react-aria-components";
import { Button } from "../../design-system/catalog/components/base/buttons/button";
import {
  dashboardPeriodLabel,
  dashboardRangeLabel,
  validDashboardRange,
  type DashboardPeriod,
  type DashboardRange,
} from "../../lib/dashboard-period";
import { monthRange, stepRange, wholePeriodName } from "../../lib/period-step";
import { formatMonthName } from "../../lib/date-format";
import "./period-picker.css";

type Props = {
  range: DashboardRange;
  period: DashboardPeriod | "custom";
  onPreset: (period: DashboardPeriod) => void;
  onRange: (range: DashboardRange) => void;
  context?: string;
};

/** Quick windows in pairs, "this" beside "last", so sixteen take eight rows. */
const PRESET_GROUPS: { heading: string; items: DashboardPeriod[] }[] = [
  { heading: "Rolling", items: ["today", "yesterday", "last-7-days", "last-30-days", "last-90-days", "last-12-months"] },
  {
    heading: "Calendar",
    items: ["week", "last-week", "month", "last-month", "quarter", "last-quarter", "year", "last-year", "year-to-date", "all"],
  },
];
const asCalendar = (range: DashboardRange): RangeValue<DateValue> | null =>
  validDashboardRange(range) ? { start: parseDate(range.from), end: parseDate(range.to) } : null;

/** The period every page shares: step it back or forward by its own length
 * with the arrows beside it, choose a quick window, a whole month, or any
 * dates. The trigger always describes the confirmed period. */
export function PeriodPicker({ range, period, onPreset, onRange, context = "Dashboard" }: Props) {
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<"days" | "months">("days");
  const [draft, setDraft] = useState<RangeValue<DateValue> | null>(() => asCalendar(range));
  const [changed, setChanged] = useState(false);
  const todayDate = localToday(getLocalTimeZone());
  const today = todayDate.toString();
  const calendarEnd = () => range.to ? parseDate(range.to) : todayDate;
  const [focused, setFocused] = useState<CalendarDate>(calendarEnd);
  const [year, setYear] = useState(focused.year);

  const bounded = validDashboardRange(range);
  // A whole year, quarter or month reads as its name ("2026", "Q3 2026"), however it was chosen.
  const name = bounded ? wholePeriodName(range) : undefined;
  const preset = name ?? dashboardPeriodLabel(period);
  const label = bounded && !name ? `${preset} · ${dashboardRangeLabel(range)}` : preset;
  const compact = bounded && !name ? dashboardRangeLabel(range, true) : "";
  const shortLabel = compact && context === "Accounts" ? compact : compact ? `${preset} · ${compact}` : preset;
  const previous = bounded ? stepRange(range, -1) : null;
  const next = bounded ? stepRange(range, 1) : null;
  const describe = (r: DashboardRange) => wholePeriodName(r) ?? dashboardRangeLabel(r);

  const openWith = (next: boolean) => {
    if (next) {
      setDraft(asCalendar(range));
      setChanged(false);
      setView("days");
      const end = calendarEnd();
      setFocused(end);
      setYear(end.year);
    }
    setOpen(next);
  };
  const choose = (r: DashboardRange) => {
    onRange(r);
    setOpen(false);
  };

  return (
    <div className="period-picker" role="group" aria-label={`${context} period`}>
      <Button
        size="sm"
        color="tertiary"
        iconLeading={ChevronLeft}
        className="period-step"
        aria-label={previous ? `Previous period: ${describe(previous)}` : "Previous period"}
        isDisabled={!previous}
        onClick={() => previous && onRange(previous)}
      />
      <DialogTrigger isOpen={open} onOpenChange={openWith}>
        <Button size="sm" color="secondary" iconLeading={CalendarIcon} className="dashboard-period" aria-label={`${context} period: ${label}`}>
          <span className="dashboard-period-full">{label}</span>
          <span className="dashboard-period-short">{shortLabel}</span>
        </Button>
        <Popover placement="bottom end" offset={8} containerPadding={12} className="vult-dropdown-popover period-popover">
          <Dialog aria-label={`Choose the ${context.toLowerCase()} period`} className="period-dialog" data-vult-surface="overlay">
            <div className="period-presets" role="group" aria-label="Quick time windows">
              {PRESET_GROUPS.map((group) => (
                <div key={group.heading} className="period-preset-group">
                  <p className="vult-type-metadata text-tertiary period-preset-heading">{group.heading}</p>
                  {group.items.map((value) => (
                    <Button
                      key={value}
                      size="sm"
                      slot={null}
                      color="tertiary"
                      aria-pressed={period === value}
                      data-selected={period === value || undefined}
                      className="period-preset"
                      onClick={() => {
                        onPreset(value);
                        setOpen(false);
                      }}
                    >
                      {dashboardPeriodLabel(value)}
                    </Button>
                  ))}
                </div>
              ))}
            </div>
            <div className="period-calendar">
              {view === "days" ? (
                <>
                  <RangeCalendar
                    aria-label="Dates"
                    value={draft}
                    onChange={(value) => {
                      setDraft(value);
                      setChanged(true);
                    }}
                    focusedValue={focused}
                    onFocusChange={(date) => setFocused(date as CalendarDate)}
                    visibleDuration={{ months: 1 }}
                    className="period-range-calendar"
                  >
                    <DaysHeader
                      onMonths={() => {
                        setYear(focused.year);
                        setView("months");
                      }}
                    />
                    <SlidingMonth today={today} />
                  </RangeCalendar>
                  <footer className="period-footer">
                    <span className="vult-type-ui text-primary period-footer-range">
                      {draft ? describe({ from: draft.start.toString(), to: draft.end.toString() }) : "Choose a start date"}
                    </span>
                    <Button size="sm" color="secondary" slot={null} onClick={() => setOpen(false)}>
                      Cancel
                    </Button>
                    <Button
                      size="sm"
                      color="primary"
                      slot={null}
                      isDisabled={!draft || !changed}
                      onClick={() => draft && choose({ from: draft.start.toString(), to: draft.end.toString() })}
                    >
                      Apply
                    </Button>
                  </footer>
                </>
              ) : (
                <MonthsView
                  year={year}
                  onYear={setYear}
                  today={today}
                  selected={range}
                  onPick={(month) => choose(monthRange(year, month))}
                  onDays={() => setView("days")}
                />
              )}
            </div>
          </Dialog>
        </Popover>
      </DialogTrigger>
      <Button
        size="sm"
        color="tertiary"
        iconLeading={ChevronRight}
        className="period-step"
        aria-label={next ? `Next period: ${describe(next)}` : "Next period"}
        isDisabled={!next || next.from > today}
        onClick={() => next && onRange(next)}
      />
    </div>
  );
}

/** Month arrows, and the month's name, which opens the twelve months and,
 * pressed again in the same place, returns to days. */
function DaysHeader({ onMonths }: { onMonths: () => void }) {
  const state = useContext(RangeCalendarStateContext);
  const start = state?.visibleRange.start;
  const title = start ? formatMonthName(start.toString()) : "";
  return (
    <header className="period-calendar-header">
      <Button
        size="sm"
        color="tertiary"
        slot={null}
        iconTrailing={ChevronDown}
        className="period-title"
        aria-label={`${title}: choose a whole month`}
        onClick={onMonths}
      >
        {title}
      </Button>
      <span className="period-arrows">
        <Button slot="previous" size="sm" color="tertiary" iconLeading={ChevronLeft} aria-label="Previous month" />
        <Button slot="next" size="sm" color="tertiary" iconLeading={ChevronRight} aria-label="Next month" />
      </span>
    </header>
  );
}

/** The month grid, sliding in from the side it was reached from. */
function SlidingMonth({ today }: { today: string }) {
  const state = useContext(RangeCalendarStateContext);
  const month = state?.visibleRange.start.toString() ?? "";
  const last = useRef(month);
  const direction = useRef(0);
  if (last.current !== month) {
    direction.current = month > last.current ? 1 : -1;
    last.current = month;
  }
  return (
    <div key={month} className="period-slide" data-direction={direction.current || undefined}>
      <CalendarGrid weekdayStyle="narrow" className="period-grid">
        <CalendarGridHeader>
          {(day) => <CalendarHeaderCell className="vult-type-metadata text-tertiary period-weekday">{day}</CalendarHeaderCell>}
        </CalendarGridHeader>
        <CalendarGridBody>
          {(date) => (
            <CalendarCell date={date} className="period-cell">
              {({ formattedDate }) => (
                <span className="period-day vult-type-ui">
                  {formattedDate}
                  {date.toString() === today && <i className="period-today" aria-hidden="true" />}
                </span>
              )}
            </CalendarCell>
          )}
        </CalendarGridBody>
      </CalendarGrid>
    </div>
  );
}

/** Twelve months of one year: one click shows a whole month. The year sits
 * where the month name was, so the same spot switches back to days. */
function MonthsView({
  year,
  onYear,
  today,
  selected,
  onPick,
  onDays,
}: {
  year: number;
  onYear: (year: number) => void;
  today: string;
  selected: DashboardRange;
  onPick: (month: number) => void;
  onDays: () => void;
}) {
  const [direction, setDirection] = useState(0);
  const names = Array.from({ length: 12 }, (_, i) => formatMonthName(`2024-${String(i + 1).padStart(2, "0")}`, "short"));
  const thisMonth = today.slice(0, 7);
  const move = (by: -1 | 1) => {
    setDirection(by);
    onYear(year + by);
  };
  return (
    <div className="period-months-view">
      <header className="period-calendar-header">
        <Button
          size="sm"
          color="tertiary"
          slot={null}
          iconTrailing={ChevronUp}
          className="period-title"
          aria-label={`${year}: back to days`}
          onClick={onDays}
        >
          {year}
        </Button>
        <span className="period-arrows">
          <Button size="sm" color="tertiary" slot={null} iconLeading={ChevronLeft} aria-label={`${year - 1}`} onClick={() => move(-1)} />
          <Button
            size="sm"
            color="tertiary"
            slot={null}
            iconLeading={ChevronRight}
            aria-label={`${year + 1}`}
            isDisabled={`${year + 1}` > today.slice(0, 4)}
            onClick={() => move(1)}
          />
        </span>
      </header>
      <div key={year} className="period-slide period-months" data-direction={direction || undefined} role="group" aria-label={`Months of ${year}`}>
        {names.map((label, i) => {
          const key = `${year}-${String(i + 1).padStart(2, "0")}`;
          const whole = monthRange(year, i + 1);
          const chosen = selected.from === whole.from && selected.to === whole.to;
          return (
            <button
              key={key}
              type="button"
              className="period-month vult-type-ui vult-focus-target"
              data-selected={chosen || undefined}
              data-current={key === thisMonth || undefined}
              aria-pressed={chosen}
              aria-label={formatMonthName(key)}
              disabled={key > thisMonth}
              onClick={() => onPick(i + 1)}
            >
              {label}
            </button>
          );
        })}
      </div>
    </div>
  );
}
