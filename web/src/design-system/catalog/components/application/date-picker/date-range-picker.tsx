"use client";

import { Fragment, type ReactNode, useMemo, useRef, useState } from "react";
import { endOfMonth, endOfWeek, getLocalTimeZone, startOfMonth, startOfWeek, today } from "@internationalized/date";
import { useControlledState } from "@react-stately/utils";
import type { RangeValue } from "@react-types/shared";
import { Calendar as CalendarIcon } from "../../../../icons";
import { useDateFormatter } from "react-aria";
import type { DateRangePickerProps as AriaDateRangePickerProps, DateValue, Placement } from "react-aria-components";
import {
    DateRangePicker as AriaDateRangePicker,
    Dialog as AriaDialog,
    Group as AriaGroup,
    Popover as AriaPopover,
    useLocale,
} from "react-aria-components";
import { Button, type ButtonProps } from "@vult/components/base/buttons/button";
import { Checkbox } from "@vult/components/base/checkbox/checkbox";
import { InputDateBase } from "@vult/components/base/input/input-date";
import { cx } from "@vult/utils/cx";
import { RangeCalendar, RangePresetButton } from "./range-calendar";

const now = today(getLocalTimeZone());

const highlightedDates = [today(getLocalTimeZone())];

export interface DateRangePickerPreset {
    group?: string;
    label: string;
    value: RangeValue<DateValue>;
}

interface DateRangePickerProps extends AriaDateRangePickerProps<DateValue> {
    size?: ButtonProps["size"];
    /** Optional visual treatment for the trigger button. */
    triggerClassName?: string;
    /** Whether to offer the supported previous-period comparison. */
    allowCompare?: boolean;
    /** Whether comparison is selected before the picker is first opened. */
    defaultCompare?: boolean;
    /** Optional presets shown beside the calendar. */
    presets?: readonly DateRangePickerPreset[];
    /** Optional label for the trigger instead of the formatted selected dates. */
    triggerLabel?: ReactNode;
    /** Relative time windows that remain distinct from calendar-date presets. */
    quickRanges?: readonly { id: string; label: string; shortLabel?: string }[];
    selectedQuickRange?: string;
    onQuickRangeSelect?: (id: string) => void;
    /** Optional compact calendar layout for a toolbar picker. */
    visibleMonths?: number;
    calendarClassName?: string;
    dialogClassName?: string;
    footerDescription?: ReactNode;
    isApplyDisabled?: boolean;
    showDateInputs?: boolean;
    popoverPlacement?: Placement;
    /** Vult's compact, neutral treatment for toolbar calendars. */
    variant?: "default" | "compact";
    /** The function to call when the apply button is clicked. */
    onApply?: (value: RangeValue<DateValue> | null, options: { compare: boolean }) => void;
    /** The function to call when the cancel button is clicked. */
    onCancel?: () => void;
}

export const DateRangePicker = ({
    allowCompare = false,
    defaultCompare = false,
    value: valueProp,
    defaultValue,
    onChange,
    onOpenChange,
    onApply,
    onCancel,
    presets: presetsProp,
    size = "sm",
    triggerClassName,
    triggerLabel,
    quickRanges,
    selectedQuickRange,
    onQuickRangeSelect,
    visibleMonths,
    calendarClassName,
    dialogClassName,
    footerDescription,
    isApplyDisabled,
    showDateInputs = true,
    popoverPlacement = "bottom right",
    variant = "default",
    ...props
}: DateRangePickerProps) => {
    const { locale } = useLocale();
    const formatter = useDateFormatter({
        month: "short",
        day: "numeric",
        year: "numeric",
    });
    const [value, setValue] = useControlledState(valueProp, defaultValue || null, onChange);
    const [focusedValue, setFocusedValue] = useState<DateValue>(valueProp?.start ?? defaultValue?.start ?? now);
    const [compare, setCompare] = useState(defaultCompare);
    const [selectingRange, setSelectingRange] = useState(false);
    const appliedValue = useRef<RangeValue<DateValue> | null>(valueProp ?? defaultValue ?? null);
    const appliedCompare = useRef(defaultCompare);

    const formattedStartDate = value?.start ? formatter.format(value.start.toDate(getLocalTimeZone())) : "Select date";
    const formattedEndDate = value?.end ? formatter.format(value.end.toDate(getLocalTimeZone())) : "Select date";

    const defaultPresets = useMemo<readonly DateRangePickerPreset[]>(
        () => [
            { label: "Today", value: { start: now, end: now } },
            { label: "Yesterday", value: { start: now.subtract({ days: 1 }), end: now.subtract({ days: 1 }) } },
            { label: "This week", value: { start: startOfWeek(now, locale), end: endOfWeek(now, locale) } },
            {
                label: "Last week",
                value: {
                    start: startOfWeek(now, locale).subtract({ weeks: 1 }),
                    end: endOfWeek(now, locale).subtract({ weeks: 1 }),
                },
            },
            { label: "This month", value: { start: startOfMonth(now), end: endOfMonth(now) } },
            {
                label: "Last month",
                value: {
                    start: startOfMonth(now).subtract({ months: 1 }),
                    end: endOfMonth(now).subtract({ months: 1 }),
                },
            },
            { label: "This year", value: { start: startOfMonth(now.set({ month: 1 })), end: endOfMonth(now.set({ month: 12 })) } },
            {
                label: "Last year",
                value: {
                    start: startOfMonth(now.set({ month: 1 }).subtract({ years: 1 })),
                    end: endOfMonth(now.set({ month: 12 }).subtract({ years: 1 })),
                },
            },
            {
                label: "All time",
                value: {
                    start: now.set({ year: 2000, month: 1, day: 1 }),
                    end: now,
                },
            },
        ],
        [locale],
    );
    const presets = presetsProp ?? (quickRanges ? [] : defaultPresets);
    const calendarPresets = useMemo(() => Object.fromEntries(presets.map((preset, index) => [`preset-${index}`, preset])), [presets]);

    const renderCalendarDialog = () => (
        <AriaDialog
            aria-label="Date range picker"
            className={cx(
                "flex flex-col rounded-lg bg-primary shadow-md ring ring-secondary_alt focus:outline-hidden",
                // Every calendar floats on the dropdown surface and elevation, clear of the page.
                variant === "compact" ? "vult-date-range-picker vult-dropdown-popover" : "vult-dropdown-popover lg:flex-row",
                dialogClassName,
            )}
            data-vult-surface="overlay"
        >
            {({ close }) => (
                <>
                    {quickRanges && (
                        <div
                            className={
                                variant === "compact"
                                    ? "vult-date-range-picker__quick"
                                    : "flex flex-wrap gap-1 border-b border-secondary p-3 lg:w-38 lg:flex-col lg:border-r lg:border-b-0"
                            }
                            role="group"
                            aria-label="Quick time windows"
                        >
                            {quickRanges.map((quick) => (
                                <Button
                                    key={quick.id}
                                    size="sm"
                                    slot={null}
                                    color={variant === "compact" ? "tertiary" : selectedQuickRange === quick.id ? "secondary" : "tertiary"}
                                    aria-label={quick.label}
                                    aria-pressed={selectedQuickRange === quick.id}
                                    className={variant === "compact" ? "vult-date-range-picker__quick-button" : "justify-start"}
                                    onClick={() => {
                                        onQuickRangeSelect?.(quick.id);
                                        close();
                                    }}
                                >
                                    {variant === "compact" ? (quick.shortLabel ?? quick.label) : quick.label}
                                </Button>
                            ))}
                        </div>
                    )}
                    {presets.length > 0 && (
                        <div className="hidden w-38 flex-col gap-0.5 border-r border-solid border-secondary p-3 lg:flex">
                            {presets.map((preset, index) => (
                                <Fragment key={`${preset.group ?? "preset"}-${preset.label}`}>
                                    {index > 0 && preset.group !== presets[index - 1]?.group ? (
                                        <div aria-hidden="true" className="mx-2 my-1.5 border-t border-secondary" />
                                    ) : null}
                                    <RangePresetButton
                                        value={preset.value}
                                        onClick={() => {
                                            setValue(preset.value);
                                            setFocusedValue(preset.value.start);
                                            appliedValue.current = preset.value;
                                            appliedCompare.current = compare;
                                            onApply?.(preset.value, { compare });
                                            close();
                                        }}
                                    >
                                        {preset.label}
                                    </RangePresetButton>
                                </Fragment>
                            ))}
                        </div>
                    )}
                    <div className="flex min-w-0 flex-col">
                        <RangeCalendar
                            focusedValue={focusedValue}
                            onFocusChange={setFocusedValue}
                            highlightedDates={highlightedDates}
                            presets={presets.length ? calendarPresets : undefined}
                            visibleDuration={visibleMonths ? { months: visibleMonths } : undefined}
                            className={calendarClassName}
                            showDateInputs={showDateInputs}
                            onSelectionPendingChange={setSelectingRange}
                            variant={variant}
                        />
                        {allowCompare ? (
                            <div className="flex min-h-14 items-center justify-between gap-4 border-t border-secondary px-4 py-3">
                                <Checkbox label="Compare" isSelected={compare} onChange={setCompare} />
                                <span className={cx("text-sm font-medium text-tertiary", !compare && "opacity-0")}>Previous period</span>
                            </div>
                        ) : null}
                        <div
                            className={cx(
                                "flex flex-wrap items-center justify-between gap-3 border-t border-secondary p-4",
                                !showDateInputs && !footerDescription && "justify-end",
                                variant === "compact" && "vult-date-range-picker__footer",
                            )}
                        >
                            {footerDescription && <div className="vult-type-metadata text-tertiary">{footerDescription}</div>}
                            {showDateInputs && (
                                <div className="hidden items-center gap-2 md:flex">
                                    <InputDateBase slot="start" size="sm" />
                                    <div className="text-md text-quaternary">–</div>
                                    <InputDateBase slot="end" size="sm" />
                                </div>
                            )}
                            <div className={variant === "compact" ? "flex gap-2" : "grid w-full grid-cols-2 gap-3 md:flex md:w-auto"}>
                                <Button
                                    size="sm"
                                    color="secondary"
                                    onClick={() => {
                                        setValue(appliedValue.current);
                                        setCompare(appliedCompare.current);
                                        onCancel?.();
                                        close();
                                    }}
                                >
                                    Cancel
                                </Button>
                                <Button
                                    size="sm"
                                    color="primary"
                                    isDisabled={isApplyDisabled || !value || selectingRange}
                                    onClick={() => {
                                        appliedValue.current = value;
                                        appliedCompare.current = compare;
                                        onApply?.(value, { compare });
                                        close();
                                    }}
                                >
                                    Apply
                                </Button>
                            </div>
                        </div>
                    </div>
                </>
            )}
        </AriaDialog>
    );

    return (
        <AriaDateRangePicker
            aria-label="Date range picker"
            shouldCloseOnSelect={false}
            {...props}
            value={value}
            onChange={setValue}
            onOpenChange={(open) => {
                if (open) setFocusedValue(value?.start ?? now);
                onOpenChange?.(open);
            }}
        >
            <AriaGroup>
                <Button size={size} color="secondary" iconLeading={CalendarIcon} className={triggerClassName}>
                    {triggerLabel ??
                        (!value ? <span className="text-placeholder">Select dates</span> : `${formattedStartDate} – ${formattedEndDate}`)}
                </Button>
            </AriaGroup>
            <AriaPopover
                placement={popoverPlacement}
                containerPadding={12}
                shouldFlip
                offset={variant === "compact" ? 12 : 8}
                className={({ isEntering, isExiting }) =>
                    cx(
                        "origin-(--trigger-anchor-point) will-change-transform",
                        isEntering &&
                            "duration-[var(--vult-motion-overlay-enter)] ease-[var(--vult-ease-settle)] animate-in fade-in placement-right:slide-in-from-left-0.5 placement-top:slide-in-from-bottom-0.5 placement-bottom:slide-in-from-top-0.5",
                        isExiting &&
                            "duration-[var(--vult-motion-overlay-exit)] ease-[var(--vult-ease-settle)] animate-out fade-out placement-right:slide-out-to-left-0.5 placement-top:slide-out-to-bottom-0.5 placement-bottom:slide-out-to-top-0.5",
                    )
                }
            >
                {renderCalendarDialog()}
            </AriaPopover>
        </AriaDateRangePicker>
    );
};
