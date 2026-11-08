"use client";
import "./styles/shell-rail.css";

import type { ReactNode } from "react";

export interface ShellHeaderFrameProps {
  sidebarToggle?: ReactNode;
  leadingControl?: ReactNode;
  leadingLabel?: string;
  children?: ReactNode;
  actions?: ReactNode;
  pageShape?: string;
  mobileNavigation?: ReactNode;
  pageIcon?: ReactNode;
  search: ReactNode;
  accountMenu: ReactNode;
}

/** Canonical shell presentation; each application supplies its own search and account operations. */
export function ShellHeaderFrame({
  sidebarToggle,
  leadingControl,
  leadingLabel,
  children,
  actions,
  pageShape,
  mobileNavigation,
  pageIcon,
  search,
  accountMenu,
}: ShellHeaderFrameProps) {
  const hasLeading = Boolean(leadingControl || leadingLabel);
  return (
    <header
      className="vult-shell-header vult-type-label relative z-30 flex h-14 shrink-0 items-center justify-between gap-2 text-primary md:gap-3"
      data-vult-page-shape={pageShape}
      data-vult-surface="main-canvas"
    >
      <div className="flex min-w-0 flex-1 items-center gap-1 md:gap-2">
        <div className="hidden shrink-0 md:flex">{sidebarToggle}</div>
        {mobileNavigation ? (
          <div className="flex shrink-0 md:hidden">{mobileNavigation}</div>
        ) : null}
        <span
          className="vult-sidebar-header-divider shrink-0"
          aria-hidden="true"
        />
        {hasLeading ? (
          <div className="vult-shell-workspace-control max-w-[55%] min-w-0 shrink-0 md:max-w-52 md:shrink">
            {leadingControl ?? (
              <span className="flex h-7 items-center px-2">{leadingLabel}</span>
            )}
          </div>
        ) : null}
        {hasLeading && children ? (
          <span
            className="vult-sidebar-header-divider vult-shell-breadcrumb-divider shrink-0"
            aria-hidden="true"
          />
        ) : null}
        {children ? (
          <div className="flex min-w-0 flex-1 items-center gap-2">
            {pageIcon ? (
              <span className="flex shrink-0 text-primary" aria-hidden="true">
                {pageIcon}
              </span>
            ) : null}
            <div className="flex min-w-0 flex-1 items-center">{children}</div>
          </div>
        ) : null}
      </div>
      <div className="vult-shell-actions flex shrink-0 items-center gap-1 md:gap-2">
        {actions}
        {search}
        <span
          className="vult-sidebar-header-divider shrink-0"
          aria-hidden="true"
        />
        {accountMenu}
      </div>
    </header>
  );
}
