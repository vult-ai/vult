// Adapted from TUFFUI (https://tuffui.com) · MIT License · Copyright (c) 2026 TUFFUI contributors
import { useReducedMotion } from "motion/react";
import "./rolling-number.css";

const DIGITS = "0123456789";

/**
 * Odometer number for an already formatted value: each digit rolls on its own
 * wheel, rightmost first. Characters are keyed from the right, so currency
 * symbols, separators and signs stay put in any regional format. The plain
 * value sits invisibly over the wheels, so it is what people select, copy and hear.
 */
export function RollingNumber({
  value,
  stagger = 24,
  className = "",
  fraction = null,
}: {
  value: string;
  stagger?: number;
  className?: string;
  /** Characters [start, end) drawn as the amount's fraction (DisplayAmount). */
  fraction?: [number, number] | null;
}) {
  const reduce = !!useReducedMotion();
  const chars = Array.from(value);
  return (
    <span className={`rolling-number ${className}`} data-fraction={fraction ? true : undefined}>
      <span aria-hidden="true" className="rolling-number-visual">
        {chars.map((char, i) => {
          const fromRight = chars.length - 1 - i;
          // Code units, as amountFraction measures; digits and separators are one each.
          const offset = chars.slice(0, i).join("").length;
          const part = fraction && offset >= fraction[0] && offset < fraction[1] ? " rolling-fraction" : "";
          return DIGITS.includes(char) ? (
            <span key={fromRight} className={`rolling-digit${part}`}>
              <span className="rolling-digit-size">0</span>
              <span
                className="rolling-digit-strip"
                style={{
                  transform: `translateY(${-Number(char) * 10}%)`,
                  transitionDuration: reduce ? "0ms" : "600ms",
                  transitionDelay: reduce ? "0ms" : `${fromRight * stagger}ms`,
                }}
              >
                {Array.from(DIGITS, (d) => <span key={d}>{d}</span>)}
              </span>
            </span>
          ) : (
            <span key={`${fromRight}${char}`} className={`rolling-char${part}`}>{char}</span>
          );
        })}
      </span>
      <span className="rolling-number-text">
        {fraction ? (
          <>
            {value.slice(0, fraction[0])}
            <span className="rolling-fraction">{value.slice(fraction[0], fraction[1])}</span>
            {value.slice(fraction[1])}
          </>
        ) : value}
      </span>
    </span>
  );
}
