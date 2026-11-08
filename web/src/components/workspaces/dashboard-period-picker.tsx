import { PeriodPicker } from "./period-picker";
import type { DashboardPeriod, DashboardRange } from "../../lib/dashboard-period";

type Props = {
  range: DashboardRange;
  period: DashboardPeriod | "custom";
  onPreset: (period: DashboardPeriod) => void;
  onRange: (range: DashboardRange) => void;
  context?: string;
};

/** The shared period for the Dashboard, Activity and Accounts (PeriodPicker). */
export function DashboardPeriodPicker(props: Props) {
  return <PeriodPicker {...props} />;
}
