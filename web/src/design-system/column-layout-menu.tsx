import { TableColumns } from "./icons";
import { Dropdown } from "./catalog/components/vult-ui/dropdown";
import { Button } from "./catalog/components/vult-ui/button";
import { Tooltip } from "./catalog/components/base/tooltip/tooltip";
import type { ColumnLayout } from "./use-column-layout";

export function ColumnLayoutMenu({ layout }: { layout: ColumnLayout }) {
  return <Dropdown.Root><Tooltip title="Columns" delay={200}><Button size="sm" color="tertiary" iconLeading={TableColumns} aria-label="Columns" /></Tooltip><Dropdown.Popover><Dropdown.Menu aria-label="Column layout" onAction={key => key === "fit" ? layout.fit() : layout.reset()} disabledKeys={layout.canFit ? [] : ["fit"]}><Dropdown.Item id="fit" label="Fit columns to view" /><Dropdown.Item id="reset" label="Reset column widths" /></Dropdown.Menu></Dropdown.Popover></Dropdown.Root>;
}
