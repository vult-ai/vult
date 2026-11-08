import type { ComponentType } from "react";

/** Quiet icon tile for empty states. */
export function EmptyStateIcon({ icon: Icon }: { icon: ComponentType<{ className?: string }> }) {
    return (
        <span
            aria-hidden="true"
            className="vult-workbench-surface-control flex size-9 items-center justify-center rounded-lg text-tertiary ring-1 ring-secondary ring-inset"
        >
            <Icon className="size-5" />
        </span>
    );
}
