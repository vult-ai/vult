import { useId, type ReactNode } from "react";
import "./settings-section.css";

export function SettingsSection({
  title,
  description,
  actions,
  children,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section className="vult-settings-section" aria-labelledby={headingId}>
      <header className="vult-settings-section-header">
        <div className="vult-settings-section-intro">
          <h2 id={headingId} className="vult-type-section-title">
            {title}
          </h2>
          {description && (
            <p className="vult-type-ui vult-settings-description">
              {description}
            </p>
          )}
        </div>
        {actions && (
          <div className="vult-settings-section-actions">{actions}</div>
        )}
      </header>
      <div className="vult-settings-rows">{children}</div>
    </section>
  );
}

export function SettingsRow({
  label,
  description,
  hint,
  htmlFor,
  children,
}: {
  label: string;
  description?: ReactNode;
  hint?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
}) {
  return (
    <div className="vult-settings-row">
      <div className="vult-settings-row-intro">
        {htmlFor ? (
          <label className="vult-type-label" htmlFor={htmlFor}>
            {label}
          </label>
        ) : (
          <p className="vult-type-label">{label}</p>
        )}
        {description && (
          <p className="vult-type-ui vult-settings-description">
            {description}
          </p>
        )}
      </div>
      <div className="vult-settings-row-content vult-type-ui">
        {children}
        {hint && (
          <p className="vult-type-metadata vult-settings-hint">{hint}</p>
        )}
      </div>
    </div>
  );
}
