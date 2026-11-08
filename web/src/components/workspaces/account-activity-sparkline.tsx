import { formatDate as dateLabel } from "../../lib/date-format";

export function Sparkline({
  values,
  max,
  name,
  from,
  to,
  last,
  shared = true,
}: {
  values: (number | null)[];
  max: number;
  name: string;
  from: string;
  to: string;
  last?: string;
  shared?: boolean;
}) {
  const Description = shared ? "title" : "desc";
  let gap = true;
  const path = values
    .map((value, index) => {
      if (value === null) {
        gap = true;
        return "";
      }
      const command = gap ? "M" : "L";
      gap = false;
      return `${command}${2 + (index / (values.length - 1)) * 172},${36 - (value / max) * 30}`;
    })
    .join(" ");
  return (
    <svg
      viewBox="0 0 176 40"
      role="img"
      aria-label={`${name}: recorded transactions per week over the last 52 weeks, ${shared ? "shared " : ""}scale zero to ${max}`}
      className="account-activity-sparkline"
      data-activity-scale={max}
    >
      <Description>
        {dateLabel(from, "full")} – {dateLabel(to, "full")}. Recorded
        transactions per week. {shared ? "Shared scale" : "Scale"}: 0–{max}.{" "}
        {last
          ? `Last entry ${dateLabel(last, "full")}.`
          : "No recorded entries."}
      </Description>
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}
