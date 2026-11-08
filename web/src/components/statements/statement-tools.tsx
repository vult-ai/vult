import { useEffect, useRef, type ChangeEventHandler, type ComponentProps, type ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";
import { Pin01, Pin02, XClose } from "../../design-system/icons";
import { SidebarToggleControl } from "../../design-system/sidebar-toggle";
import { Select as SourceSelect } from "../../design-system/catalog/components/base/select/select";
import { Tooltip } from "../../design-system/catalog/components/base/tooltip/tooltip";
import { Button } from "../ui/button";
import "./statement-tools.css";

/** Report parameters use the inspector Select. A hidden native select keeps
 * the standard change event that report controls listen for. */
export function StatementToolSelect({ label, value, options, onChange, collapsed, icon, disabled, "aria-label": ariaLabel }: {
  label: string;
  value: string;
  options: readonly { value: string; label: string; disabled?: boolean }[];
  onChange: ChangeEventHandler<HTMLSelectElement>;
  collapsed: boolean;
  icon: ComponentProps<typeof SourceSelect>["icon"];
  disabled?: boolean;
  "aria-label": string;
}) {
  const native = useRef<HTMLSelectElement>(null);
  return <div className="statement-tool-select">
    <select ref={native} className="sr-only" tabIndex={-1} aria-hidden="true" aria-label={ariaLabel} value={value} disabled={disabled} onChange={onChange}>
      {options.map(option => <option key={option.value} value={option.value} disabled={option.disabled}>{option.label}</option>)}
    </select>
    <SourceSelect label={label} aria-label={ariaLabel} variant="inspector" inspectorSide="right" isCollapsed={collapsed} size="sm" value={value} isDisabled={disabled} icon={icon}
      items={options.map(option => ({ id: option.value, label: option.label, isDisabled: option.disabled }))}
      onChange={key => {
        const control = native.current, next = String(key);
        if (!control || disabled || next === control.value || options.find(option => option.value === next)?.disabled) return;
        control.value = next;
        control.dispatchEvent(new Event("input", { bubbles: true }));
        control.dispatchEvent(new Event("change", { bubbles: true }));
      }}>
      {item => <SourceSelect.Item {...item} />}
    </SourceSelect>
  </div>;
}

export function StatementToolAction({ label, icon, collapsed, onClick, disabled, expanded }: {
  label: string; icon: ReactNode; collapsed: boolean; onClick: (trigger: HTMLButtonElement) => void; disabled?: boolean; expanded?: boolean;
}) {
  return <Tooltip title={label} placement="left" isDisabled={!collapsed || !!expanded}>
    <AriaButton className="statement-tool-action vult-focus-target" aria-label={label} aria-expanded={expanded} isDisabled={disabled} onPress={event => onClick(event.target as HTMLButtonElement)}>
      {icon}<span>{label}</span>
    </AriaButton>
  </Tooltip>;
}

export function StatementTools({ expanded, animate, pinned, onToggle, onPin, onClose, panelOpen, panelLabel, children, panel, footer }: {
  expanded: boolean; animate: boolean; pinned: boolean; onToggle: (animate: boolean) => void; onPin: () => void;
  onClose: (restoreFocus: boolean) => void; panelOpen: boolean; panelLabel: string;
  children: ReactNode; panel: ReactNode; footer: ReactNode;
}) {
  const host = useRef<HTMLElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!panelOpen || pinned) return;
    const outside = (event: PointerEvent) => {
      // Select/calendar portals and entry/evidence dialogs belong to this
      // investigation. Closing them must not dismiss the report work panel.
      if (document.querySelector('[role="dialog"], [role="alertdialog"], [role="listbox"]')) return;
      if (event.target instanceof Node && !host.current?.contains(event.target)) onClose(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [panelOpen, pinned, onClose]);
  return <aside ref={host} className="statement-work-tools" data-expanded={expanded} data-pinned={pinned && panelOpen} data-motion={animate ? "pointer" : "instant"} aria-label="Statement tools"
    onKeyDown={event => {
      if (event.key !== "Escape" || event.defaultPrevented || document.querySelector('[role="dialog"], [role="alertdialog"], [role="listbox"]')) return;
      if (panelOpen) { event.preventDefault(); onClose(true); }
      else if (expanded) { event.preventDefault(); onToggle(false); toggle.current?.focus({ preventScroll: true }); }
    }}>
    <div className="statement-tools-rail">
      <header className="statement-tools-header"><SidebarToggleControl buttonRef={toggle} expanded={expanded || (pinned && panelOpen)} onToggle={onToggle} side="right" controls="statement-tools-controls" expandLabel="Expand statement tools" collapseLabel="Collapse statement tools" /></header>
      <div id="statement-tools-controls" className="statement-tools-controls"><div className="statement-tools-scroll">{children}</div><div className="statement-tools-footer">{footer}</div></div>
    </div>
    <section className="statement-work-panel" hidden={!panelOpen} aria-label={panelLabel}>
      <header className="statement-work-panel-header"><h3>{panelLabel}</h3><div>
        <Button color="tertiary" size="sm" className="statement-panel-icon" aria-label={pinned ? "Unpin work panel" : "Pin work panel"} aria-pressed={pinned} onClick={onPin}>{pinned ? <Pin02 aria-hidden /> : <Pin01 aria-hidden />}</Button>
        <Button color="tertiary" size="sm" className="statement-panel-icon" aria-label="Close work panel" onClick={() => onClose(true)}><XClose aria-hidden /></Button>
      </div></header>
      <div className="statement-work-panel-body" data-vult-view-scroll>{panel}</div>
    </section>
  </aside>;
}
