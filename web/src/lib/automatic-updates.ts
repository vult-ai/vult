/** What the update installer on the owner's own computer reports. */
export type InstallerReport = {
  enabled: boolean;
  installed?: { version: string; schema: string };
  state?: string;
  installing?: string;
  last_outcome?: string;
  last_reason?: string;
  requested?: string;
  checking?: boolean;
  problems?: string[];
};

export type UpdateStatus = {
  current?: string;
  latest?: string;
  available: boolean;
  /** Present where the owner can install from Vult. */
  installer?: InstallerReport;
};

/** Whether an update the owner asked for is under way. */
export function updateUnderWay(installer: InstallerReport | null | undefined) {
  return !!installer && (!!installer.requested || (!!installer.state && installer.state !== "idle"));
}

/** Whether the last update the owner asked for didn't finish. */
export function lastUpdateUnfinished(installer: InstallerReport | null | undefined) {
  const outcome = installer?.last_outcome;
  return outcome === "failed" || outcome === "restored" || outcome === "deferred";
}
