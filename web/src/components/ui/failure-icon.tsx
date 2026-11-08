import {
  AlertCircle,
  AlertOctagon,
  AlertTriangle,
  FileX02,
  Lock01,
  WifiOff,
} from "../../design-system/icons";
import type { FailureIcon as FailureIconName } from "../../lib/errors";

const icons = {
  connection: WifiOff,
  file: FileX02,
  access: Lock01,
  attention: AlertTriangle,
  critical: AlertOctagon,
  generic: AlertCircle,
} as const;

/** The one icon set for every failure surface. The policy picks the name. */
export function FailureIcon({
  name,
  className,
}: {
  name: FailureIconName;
  className?: string;
}) {
  const Icon = icons[name];
  return <Icon className={className} aria-hidden="true" />;
}
