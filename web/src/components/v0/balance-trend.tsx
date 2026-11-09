import { formatDateRange } from "../../lib/date-format";
import { useId } from "react";
import { motion, useReducedMotion } from "motion/react";
import { money as baseMoney, type Book } from "../../lib/api";
import { hiddenAmount, useAmountsHidden } from "../../lib/amount-privacy";
import "./balance-trend.css";

export type BalanceTrendPoint = { date: string; amount: string };
type Range = { from: string; to: string };
const dateNumber = (value: string) => Date.parse(value + "T00:00:00Z");

/** A shared monetary domain for two recorded periods, with no invented history. */
export function BalanceTrend({
  name,
  current = [],
  previous = [],
  range,
  previousRange,
  book,
}: {
  name: string;
  current?: BalanceTrendPoint[];
  previous?: BalanceTrendPoint[];
  range?: Range;
  previousRange?: Range;
  book: Book;
}) {
  const gradient = useId();
  const descriptionId = `${gradient}-description`;
  const reduced = useReducedMotion();
  const transition = {
    duration: reduced ? 0 : 0.18,
    ease: [0.16, 1, 0.3, 1] as [number, number, number, number],
  };
  const all = [...current, ...previous].map((point) => BigInt(point.amount));
  const min = all.reduce((a, b) => (b < a ? b : a), all[0] || 0n);
  const max = all.reduce((a, b) => (b > a ? b : a), all[0] || 0n);
  const coordinates = (points: BalanceTrendPoint[], window?: Range) => {
    if (!points.length) return [];
    const from = dateNumber(window?.from || points[0].date);
    const to = dateNumber(window?.to || points.at(-1)!.date);
    return points.map((point) => ({
      x:
        to === from
          ? 110
          : 4 + ((dateNumber(point.date) - from) / (to - from)) * 212,
      y:
        max === min
          ? 24
          : 44 -
            (Number(((BigInt(point.amount) - min) * 1_000_000n) / (max - min)) /
              1_000_000) *
              40,
    }));
  };
  const curve = (points: { x: number; y: number }[]) =>
    points
      .map((point, index) => {
        if (!index) return `M${point.x},${point.y}`;
        const prior = points[index - 1],
          middle = (prior.x + point.x) / 2;
        return `C${middle},${prior.y} ${middle},${point.y} ${point.x},${point.y}`;
      })
      .join(" ");
  const now = coordinates(current, range),
    prior = coordinates(previous, previousRange);
  const path = curve(now),
    previousPath = curve(prior);
  const last = now.at(-1);
  const hidden = useAmountsHidden();
  const money = (minor: string, b: Book) => (hidden ? hiddenAmount : baseMoney(minor, b));
  const title = current.length
    ? `${name}: ${formatDateRange(current[0].date, current.at(-1)!.date, "full")}, ending ${money(current.at(-1)!.amount, book)}. ${previous.length ? `Previous period: ${formatDateRange(previous[0].date, previous.at(-1)!.date, "full")}, ending ${money(previous.at(-1)!.amount, book)}.` : "Previous-period history unavailable."}`
    : `${name}: recorded trend unavailable.`;
  return (
    <div
      className="overview-metric-trend balance-trend"
      role="img"
      aria-label={title}
      aria-describedby={descriptionId}
    >
      <svg
        className="balance-trend-plot"
        viewBox="0 0 220 48"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <desc id={descriptionId}>
          The solid line with a filled endpoint is the selected period; the
          dashed gray line is the previous period. Both use the same scale.
        </desc>
        <defs>
          <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
            <stop
              offset="0%"
              stopColor="var(--color-chart-blue)"
              stopOpacity="0.12"
            />
            <stop
              offset="100%"
              stopColor="var(--color-chart-blue)"
              stopOpacity="0"
            />
          </linearGradient>
        </defs>
        {prior.length > 1 && (
          <motion.path
            key={`previous-${prior.length}`}
            data-trend-previous
            initial={false}
            animate={{ d: previousPath }}
            transition={transition}
            className="balance-trend-previous"
            vectorEffect="non-scaling-stroke"
          />
        )}
        {prior.length === 1 && (
          <circle
            cx={prior[0].x}
            cy={prior[0].y}
            r="2"
            className="balance-trend-previous-point"
          />
        )}
        {now.length > 1 && (
          <>
            <motion.path
              key={`area-${now.length}`}
              initial={false}
              animate={{ d: `${path} L${last!.x},48 L${now[0].x},48 Z` }}
              transition={transition}
              fill={`url(#${gradient})`}
            />
            <motion.path
              key={`current-${now.length}`}
              data-trend-current
              initial={false}
              animate={{ d: path }}
              transition={transition}
              className="balance-trend-current"
              vectorEffect="non-scaling-stroke"
            />
          </>
        )}
        {last && (
          <motion.circle
            initial={false}
            animate={{ cx: last.x, cy: last.y }}
            transition={transition}
            r="2.5"
            className="balance-trend-current-point"
          />
        )}
      </svg>
      {!now.length && (
        <span
          className="balance-trend-empty vult-type-metadata"
          aria-hidden="true"
        >
          History unavailable
        </span>
      )}
    </div>
  );
}
