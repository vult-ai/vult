import { useEffect, useState } from "react";
import { InputBase } from "./catalog/components/base/input/input";

/** Keep editing separate from navigation, especially for multi-digit pages. */
export function PageJump({
  currentPage,
  pageCount,
  onPageChange,
  isDisabled = false,
}: {
  currentPage: number;
  pageCount: number;
  onPageChange: (page: number) => void;
  isDisabled?: boolean;
}) {
  const [draft, setDraft] = useState(String(currentPage));
  useEffect(() => setDraft(String(currentPage)), [currentPage, pageCount]);

  const commit = () => {
    if (isDisabled) return;
    const value = /^\d+$/.test(draft.trim()) ? Number(draft) : NaN;
    const next = Number.isSafeInteger(value)
      ? Math.max(1, Math.min(pageCount, value))
      : currentPage;
    setDraft(String(next));
    if (next !== currentPage) onPageChange(next);
  };

  return (
    <InputBase
      aria-label="Go to page"
      title={`Enter a page from 1 to ${pageCount.toLocaleString("en-US")} and press Enter`}
      inputClassName="px-2! py-1.5! text-center tabular-nums"
      inputMode="numeric"
      isDisabled={isDisabled}
      disabled={isDisabled}
      onChange={(event) => setDraft(event.target.value)}
      onFocus={(event) => event.currentTarget.select()}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          commit();
        } else if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          setDraft(String(currentPage));
          event.currentTarget.select();
        }
      }}
      size="sm"
      type="text"
      value={draft}
      wrapperClassName={`vult-compact-field ${String(pageCount).length > 3 ? "w-16" : "w-12"}`}
    />
  );
}
