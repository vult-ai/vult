import type { ComponentType } from "react";

export type NavigationItem = {
  id: string;
  label: string;
  icon: ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  disabled?: boolean;
  href?: string;
  /** Things waiting for the owner here, such as approvals. Shown only when above zero. */
  count?: number;
  /** Starts a new group, set off from the items above by a gap. */
  groupStart?: boolean;
};

/** The same descriptors and renderer serve the desktop rail and mobile drawer. */
export function Navigation({
  items,
  active,
  onNavigate,
}: {
  items: readonly NavigationItem[];
  active: string;
  onNavigate?: (id: string) => void;
}) {
  return (
    <nav aria-label="Book navigation">
      <ul className="shell-navigation-list">
        {items.map(({ id, label, icon: Icon, disabled, href, count, groupStart }) => {
          const Control = href ? "a" : "button";
          return (
            <li className="vult-navigation-item" key={id} data-group-start={groupStart || undefined}>
              <Control
                {...(href
                  ? { href, "aria-disabled": disabled || undefined }
                  : { type: "button" as const, disabled })}
                className="shell-navigation-link vult-action-target vult-focus-target vult-focus-contained"
                data-disabled={disabled || undefined}
                aria-current={active === id ? "page" : undefined}
                aria-label={count ? `${label}, ${count} waiting` : label}
                onClick={(event) => {
                  if (disabled) {
                    event.preventDefault();
                    return;
                  }
                  if (
                    event.ctrlKey ||
                    event.metaKey ||
                    event.shiftKey ||
                    event.altKey
                  )
                    return;
                  if (onNavigate) event.preventDefault();
                  onNavigate?.(id);
                }}
              >
                <span>
                  <Icon className="size-4" aria-hidden />
                  <span className="vult-rail-label">{label}</span>
                  {!!count && <span className="shell-navigation-count vult-type-metadata" aria-hidden="true">{count > 99 ? "99+" : count}</span>}
                </span>
              </Control>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
