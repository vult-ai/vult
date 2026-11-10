import { useRef, type ReactNode } from "react";
import type { Key } from "react-aria-components";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { ChevronDown } from "../../design-system/icons";
import "./statement-compact-tools.css";

type Option = { value: string; label: string; disabled?: boolean };
export type CompactAction = { id: string; label: string; disabled?: boolean };

/** Statements' tools on narrow screens: one labeled control for each common
 * choice (report, date, comparison) and one More menu for everything else.
 * Every control names itself in text, so nothing depends on a tooltip. */
export function StatementCompactTools({ report, reports, onReport, period, comparison, comparisons, comparisonDisabled, onComparison, columns, columnsDisabled, monthsDisabled, onColumns, actions, onAction }: {
  report: string;
  reports: { label: string; options: Option[] }[];
  onReport: (value: string) => void;
  period: ReactNode;
  comparison: string;
  comparisons: Option[];
  comparisonDisabled?: boolean;
  onComparison: (value: string, trigger: HTMLElement) => void;
  columns: string;
  columnsDisabled?: boolean;
  monthsDisabled?: boolean;
  onColumns: (value: string) => void;
  actions: { label: string; items: CompactAction[] }[];
  onAction: (id: string, trigger: HTMLElement) => void;
}) {
  const compareTrigger = useRef<HTMLButtonElement>(null);
  const moreTrigger = useRef<HTMLButtonElement>(null);
  const reportLabel = reports.flatMap(group => group.options).find(option => option.value === report)?.label ?? "Report";
  const comparisonLabel = comparisons.find(option => option.value === comparison)?.label ?? "None";
  const pick = (keys: "all" | Set<Key>) => (keys === "all" ? undefined : [...keys][0]?.toString());
  return <div className="statement-compact-tools" role="toolbar" aria-label="Report">
    <Dropdown.Root>
      <Button size="sm" color="secondary" className="statement-compact-control statement-compact-report" iconTrailing={ChevronDown} aria-label={`Report: ${reportLabel}`}>
        <span className="statement-compact-value">{reportLabel}</span>
      </Button>
      <Dropdown.Popover appearance="control" placement="bottom start">
        <Dropdown.Menu aria-label="Choose report" selectionMode="single" disallowEmptySelection selectedKeys={[report]}
          disabledKeys={reports.flatMap(group => group.options.filter(option => option.disabled).map(option => option.value))}
          onSelectionChange={keys => { const next = pick(keys); if (next && next !== report) onReport(next); }}>
          {reports.map(group => <Dropdown.Section key={group.label} aria-label={group.label}>
            <Dropdown.SectionHeader className="statement-compact-menu-heading">{group.label}</Dropdown.SectionHeader>
            {group.options.map(option => <Dropdown.Item key={option.value} id={option.value} appearance="control">{option.label}</Dropdown.Item>)}
          </Dropdown.Section>)}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown.Root>
    <div className="statement-compact-period">{period}</div>
    <Dropdown.Root>
      <Button ref={compareTrigger} size="sm" color="secondary" className="statement-compact-control statement-compact-compare" iconTrailing={ChevronDown} isDisabled={comparisonDisabled} aria-label={`Compare: ${comparisonLabel}`}>
        <span className="statement-compact-name">Compare</span><span className="statement-compact-value">{comparisonLabel}</span>
      </Button>
      <Dropdown.Popover appearance="control" placement="bottom start">
        <Dropdown.Menu aria-label="Compare report" selectionMode="single" disallowEmptySelection selectedKeys={[comparison]}
          disabledKeys={comparisons.filter(option => option.disabled).map(option => option.value)}
          onAction={key => { const next = String(key); if (compareTrigger.current) onComparison(next, compareTrigger.current); }}>
          {comparisons.map(option => <Dropdown.Item key={option.value} id={option.value} appearance="control">{option.label}</Dropdown.Item>)}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown.Root>
    <Dropdown.Root>
      <Button ref={moreTrigger} size="sm" color="secondary" className="statement-compact-control statement-compact-more" iconTrailing={ChevronDown}>More</Button>
      <Dropdown.Popover appearance="control" placement="bottom end">
        <Dropdown.Menu aria-label="More report tools" onAction={key => { if (moreTrigger.current) onAction(String(key), moreTrigger.current); }}>
          <Dropdown.Section aria-label="Columns" selectionMode="single" disallowEmptySelection selectedKeys={[`columns:${columns}`]}
            onSelectionChange={keys => { const next = pick(keys)?.replace("columns:", ""); if (next && next !== columns) onColumns(next); }}>
            <Dropdown.SectionHeader className="statement-compact-menu-heading">Columns</Dropdown.SectionHeader>
            <Dropdown.Item id="columns:summary" appearance="control" isDisabled={columnsDisabled}>Summary</Dropdown.Item>
            <Dropdown.Item id="columns:months" appearance="control" isDisabled={columnsDisabled || monthsDisabled}>Months</Dropdown.Item>
          </Dropdown.Section>
          {actions.map(section => <Dropdown.Section key={section.label} aria-label={section.label}>
            <Dropdown.SectionHeader className="statement-compact-menu-heading">{section.label}</Dropdown.SectionHeader>
            {section.items.map(item => <Dropdown.Item key={item.id} id={item.id} appearance="control" isDisabled={item.disabled}>{item.label}</Dropdown.Item>)}
          </Dropdown.Section>)}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown.Root>
  </div>;
}
