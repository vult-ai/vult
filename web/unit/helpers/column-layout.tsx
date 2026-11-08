import { useRef } from "react";
import { useColumnLayout } from "../../src/design-system/use-column-layout";
import { ColumnResizer } from "../../src/design-system/column-resizer";
import { ColumnLayoutMenu } from "../../src/design-system/column-layout-menu";
export { ColumnLayoutScope } from "../../src/design-system/use-column-layout";
export { UserPreferencesProvider } from "../../src/lib/user-preferences-context";
export { RecordsTable } from "../../src/design-system/catalog/components/application/table/records-table";
export function ColumnHarness({ available = 600, view = "records", hidden = false }: { available?: number; view?: string; hidden?: boolean }) {
  const scroll = useRef<HTMLDivElement>(null);
  const columns = [{ id: "name", min: 100, preferred: 200 }, ...hidden ? [] : [{ id: "amount", min: 128, preferred: 184, grow: false }], { id: "actions", min: 48, preferred: 48, fixed: true }];
  const layout = useColumnLayout({ columns, view, available, scroll });
  return <div ref={scroll} data-widths={JSON.stringify(layout.widths)}><ColumnLayoutMenu layout={layout} />{columns.filter(column => !column.fixed).map(column => <ColumnResizer key={column.id} layout={layout} id={column.id} label={column.id} />)}</div>;
}
