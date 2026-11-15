import { fetchJSON, fetchResponse, readResponse } from "./errors";
import { withRecentSignIn } from "./recent-sign-in";
import { writeHeaders } from "./session";

/* The Vult's own backups: the latest one, kept before an update, and
   putting it back. Putting it back replaces everything since. */

export type Backup = {
  id: string;
  created_at: string;
  /** The version the copy holds; may be empty. */
  version: string;
  /** The version installed after it. */
  to?: string;
  reason: "upgrade" | "update" | string;
  size?: number;
  /** A ready sentence saying what restoring loses. */
  lost: string;
};
export type BackupsRead = {
  /** False where this Vult can't restore; Restore stays hidden. */
  available: boolean;
  backup: Backup | null;
  /** Shown once after an upgrade. */
  notice: { backup_id: string; version: string; backed_up_at: string } | null;
};
export type RestoreJob = { state: "running" | "done" | "failed"; step?: string; error?: string };

/** The word a restore is confirmed with, typed. */
export const RESTORE_WORD = "RESTORE";

async function post<T>(path: string, body: unknown): Promise<T> {
  const response = await fetchResponse(path, { method: "POST", headers: await writeHeaders(), body: JSON.stringify(body) });
  return readResponse<T>(response, "write");
}

export const readBackups = (signal?: AbortSignal) =>
  fetchJSON<BackupsRead>("/api/v1/backups", { cache: "no-store", ...(signal ? { signal } : {}) });

/** Hides the after-upgrade notice for good, for this backup. */
export const dismissUpgradeNotice = (backup_id: string) => post<unknown>("/api/v1/backups/notice-seen", { backup_id });

/** Starts putting a backup back; a recent passkey sign-in confirms it first. */
export const startRestore = (backup_id: string) =>
  withRecentSignIn(() => post<{ job_id: string }>("/api/v1/backups/restore", { backup_id, confirm: RESTORE_WORD }));

export const readRestore = (job: string, signal?: AbortSignal) =>
  fetchJSON<RestoreJob>(`/api/v1/backups/restore/${encodeURIComponent(job)}`, { cache: "no-store", ...(signal ? { signal } : {}) });
