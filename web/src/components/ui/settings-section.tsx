import { useId, type ComponentType, type ReactNode } from "react";
import { ChevronDown } from "../../design-system/icons";
import "./settings-section.css";

export function SettingsSection({
  title,
  icon: Icon,
  description,
  actions,
  children,
}: {
  title: string;
  /** The section's icon, in the same tile as the Agents panel. */
  icon?: ComponentType<{ "aria-hidden"?: boolean }>;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section className="vult-settings-section" aria-labelledby={headingId}>
      <header className="vult-settings-section-header">
        {Icon && (
          <span className="vult-settings-section-icon" aria-hidden="true">
            <Icon aria-hidden />
          </span>
        )}
        <div className="vult-settings-section-intro">
          <h2 id={headingId} className="vult-type-label">
            {title}
          </h2>
          {description && (
            <p className="vult-type-metadata vult-settings-description">
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

/**
 * A group that opens in place: a card whose summary row has the group's name,
 * an optional count and a chevron, and whose body follows in the same card.
 * Settings uses it for every collapsible group so they all look and behave
 * alike.
 */
export function SettingsDisclosure({
  title,
  meta,
  className = "",
  children,
}: {
  title: string;
  meta?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <details className={`vult-settings-disclosure ${className}`.trim()}>
      <summary className="vult-settings-disclosure-summary vult-focus-target">
        <span className="vult-type-label">{title}</span>
        {meta != null && <span className="vult-settings-disclosure-meta vult-type-metadata">{meta}</span>}
        <ChevronDown className="vult-settings-disclosure-chevron" aria-hidden />
      </summary>
      <div className="vult-settings-disclosure-body">{children}</div>
    </details>
  );
}
