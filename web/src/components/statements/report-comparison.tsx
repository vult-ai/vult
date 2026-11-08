import { useState } from "react";
import { parseDate, type CalendarDate } from "@internationalized/date";
import { DateField } from "react-aria-components";
import { InputDateBase } from "../../design-system/catalog/components/base/input/input-date";
import { Dialog } from "../ui/dialog";
import { Button } from "../ui/button";
import { Field } from "../form";
import type { ReportLocation } from "../../lib/financial-reports";

export function ReportComparison({
  selection,
  position,
  returnTo,
  onClose,
  onApply,
}: {
  selection: ReportLocation;
  position: boolean;
  returnTo: HTMLElement;
  onClose: () => void;
  onApply: (value: {
    comparison: "explicit";
    compareFrom?: string;
    compareTo: string;
  }) => void;
}) {
  const [from, setFrom] = useState<CalendarDate | null>(() =>
    parseDate(
      selection.compareFrom ||
        parseDate(selection.range.from).subtract({ years: 1 }).toString(),
    ),
  );
  const [to, setTo] = useState<CalendarDate | null>(() =>
    parseDate(
      selection.compareTo ||
        parseDate(selection.range.to).subtract({ years: 1 }).toString(),
    ),
  );
  return (
    <Dialog
      open
      title={position ? "Comparison date" : "Comparison period"}
      description={
        position
          ? "Compare positions at two dates. Each balance includes its supported opening position and postings through that date."
          : "Compare performance over two explicit inclusive periods."
      }
      returnTo={returnTo}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <div className="statement-comparison-form">
        {!position && (
          <Field label="Comparison begins">
            <DateField
              aria-label="Comparison begins"
              value={from}
              onChange={setFrom}
            >
              <InputDateBase size="sm" />
            </DateField>
          </Field>
        )}
        <Field label={position ? "Compare as of" : "Comparison ends"}>
          <DateField
            aria-label={position ? "Compare as of" : "Comparison ends"}
            value={to}
            onChange={setTo}
          >
            <InputDateBase size="sm" />
          </DateField>
        </Field>
        <div className="statement-pack-actions">
          <Button size="sm" color="tertiary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={!to || (!position && (!from || from.compare(to) > 0))}
            onClick={() => {
              if (to)
                onApply({
                  comparison: "explicit",
                  compareFrom: position ? undefined : from?.toString(),
                  compareTo: to.toString(),
                });
            }}
          >
            Apply comparison
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
