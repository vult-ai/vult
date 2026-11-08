import type { ReactNode } from "react";

export function PageToolbar({ actions }: { actions?: ReactNode }) {
  if (!actions) return null;
  return <div className="workspace-toolbar workspace-actions">{actions}</div>;
}

export function Surface({
  title,
  titleAccessory,
  description,
  actions,
  children,
  className = "",
}: {
  title: string;
  titleAccessory?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`workspace-surface ${className}`}>
      <header>
        <div>
          {titleAccessory ? (
            <div className="flex items-center gap-2"><h3>{title}</h3>{titleAccessory}</div>
          ) : <h3>{title}</h3>}
          {description && <p>{description}</p>}
        </div>
        {actions}
      </header>
      {children}
    </section>
  );
}

export function Statistic({
  label,
  value,
  children,
}: {
  label: string;
  value: string;
  children?: ReactNode;
}) {
  return (
    <div className="workspace-stat">
      <span className="workspace-stat-label">{label}</span>
      <strong className="vult-money">{value}</strong>
      {children}
    </div>
  );
}
