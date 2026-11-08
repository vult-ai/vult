"use client";

import { type Ref, useState } from "react";
import { cn as cx } from "../lib/utils";
import "./styles/shell-rail.css";

export function SidebarIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path className="vult-sidebar-toggle-divider" d="M10 4v16" />
    </svg>
  );
}

export function SidebarToggleControl({
  expanded,
  onToggle,
  collapseLabel = "Collapse sidebar",
  expandLabel = "Expand sidebar",
  controls,
  side = "left",
  buttonRef,
  className,
}: {
  expanded: boolean;
  onToggle: (animate: boolean) => void;
  collapseLabel?: string;
  expandLabel?: string;
  controls?: string;
  side?: "left" | "right";
  buttonRef?: Ref<HTMLButtonElement>;
  className?: string;
}) {
  const [previewExpanded, setPreviewExpanded] = useState<boolean | null>(null);
  const [animateIcon, setAnimateIcon] = useState(true);
  const label = expanded ? collapseLabel : expandLabel;

  return (
    <button
      ref={buttonRef}
      type="button"
      onPointerEnter={(event) => {
        if (event.pointerType !== "mouse") return;
        setAnimateIcon(true);
        setPreviewExpanded(!expanded);
      }}
      onPointerLeave={() => {
        setAnimateIcon(true);
        setPreviewExpanded(null);
      }}
      onPointerCancel={() => setPreviewExpanded(null)}
      onClick={(event) => {
        setAnimateIcon(event.detail !== 0);
        setPreviewExpanded(null);
        onToggle(event.detail !== 0);
      }}
      aria-label={label}
      aria-expanded={expanded}
      aria-controls={controls}
      title={label}
      data-animate={animateIcon}
      data-icon-expanded={previewExpanded ?? expanded}
      className={cx(
        "vult-sidebar-toggle vult-focus-target flex size-8 shrink-0 items-center justify-center rounded-md text-secondary",
        className,
      )}
    >
      <SidebarIcon className={side === "right" ? "-scale-x-100" : undefined} />
    </button>
  );
}
