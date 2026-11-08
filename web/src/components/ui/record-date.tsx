import {
  dateISO,
  formatDate,
  type DateFormat,
  type DatePreferences,
} from "../../lib/date-format";

/** Shared record-date display: compact in lists, always complete in details. */
export function RecordDate({
  value,
  format = "compact",
  className,
  preferences,
}: {
  value: string | null | undefined;
  format?: DateFormat;
  className?: string;
  preferences?: DatePreferences;
}) {
  const iso = dateISO(value);
  if (!iso) return <span className={className}>{value || "—"}</span>;
  const visible = formatDate(value, format, new Date(), preferences);
  return (
    <time className={className} dateTime={iso} title={value || undefined}>
      {format === "full" ? (
        visible
      ) : (
        <>
          <span aria-hidden="true">{visible}</span>
          <span className="sr-only">
            {formatDate(value, "full", new Date(), preferences)}
          </span>
        </>
      )}
    </time>
  );
}
