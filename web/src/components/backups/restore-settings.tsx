import { useEffect, useRef, useState } from "react";
import { ClockCounterClockwise, Fingerprint } from "../../design-system/icons";
import { formatDate } from "../../lib/date-format";
import { readBackups, readRestore, RESTORE_WORD, startRestore, type Backup, type BackupsRead, type RestoreJob } from "../../lib/backups";
import { Field } from "../form";
import { Button } from "../ui/button";
import { ConfirmFooter } from "../ui/confirm-action";
import { Dialog } from "../ui/dialog";
import { ErrorNotice } from "../ui/error-notice";
import { SettingsRow, SettingsSection } from "../ui/settings-section";
import "./backups.css";

/** A backup's moment, as people say it: "Nov 12, 2026 at 7:58 PM". */
export function backupMoment(at: string) {
  const time = new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return `${formatDate(at.slice(0, 10), "full")} at ${time}`;
}

/**
 * Vult settings → Restore: the latest backup, and one button that puts the
 * whole Vult back as it was then. What's lost is said plainly first, and the
 * owner's passkey and a typed word confirm it.
 */
/** Where a link lands on Restore: Settings → Data, at this section. */
export const restoreSection = "restore";

export function RestoreSettings({ active, read = readBackups, start = startRestore, poll = readRestore }: {
  active: boolean;
  read?: typeof readBackups;
  start?: typeof startRestore;
  poll?: typeof readRestore;
}) {
  const [latest, setLatest] = useState<BackupsRead | null>(null);
  const [problem, setProblem] = useState<unknown>(null);
  const [confirming, setConfirming] = useState(false);
  useEffect(() => {
    if (!active) return;
    const c = new AbortController();
    read(c.signal).then(setLatest, (e) => { if (!c.signal.aborted) setProblem(e); });
    return () => c.abort();
  }, [active, read]);
  // Arriving from the upgrade notice: Restore is below the fold on a phone,
  // so bring it into view once it has its backup.
  useEffect(() => {
    if (latest && window.location.hash === `#${restoreSection}`)
      document.getElementById(restoreSection)?.scrollIntoView({ block: "start" });
  }, [latest]);
  const backup = latest?.backup;
  return (
    <SettingsSection id={restoreSection} title="Restore" icon={ClockCounterClockwise} description="Your Vult’s latest backup. Restoring it replaces everything since.">
      <ErrorNotice error={problem} intent="read" />
      {latest && !backup && <SettingsRow label="Latest backup"><span className="text-tertiary">No backup yet</span></SettingsRow>}
      {backup && (
        <SettingsRow label="Latest backup" description={[backupMoment(backup.created_at), backup.version ? `Vult ${backup.version}` : "", backup.to ? `kept before updating to ${backup.to}` : ""].filter(Boolean).join(" · ")}>
          <Button size="sm" color="secondary" onClick={() => setConfirming(true)}>Restore this backup</Button>
        </SettingsRow>
      )}
      {backup && confirming && (
        <RestoreDialog backup={backup} start={start} poll={poll} onClose={() => setConfirming(false)} />
      )}
    </SettingsSection>
  );
}

function RestoreDialog({ backup, start, poll, onClose }: {
  backup: Backup;
  start: typeof startRestore;
  poll: typeof readRestore;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<unknown>(null);
  const [job, setJob] = useState<string | null>(null);
  const [state, setState] = useState<RestoreJob | null>(null);
  const timer = useRef<number>(0);
  // While it runs, its progress is read every second. It isn't there for the
  // first few seconds, and Vult stops on the way, so a read that fails just
  // waits for the next one. Signed out at the end (the backup's own sign-ins
  // return) means it finished.
  useEffect(() => {
    if (!job) return;
    let current = true;
    const tick = () => poll(job).then(
      (s) => { if (!current) return; setState(s); if (s.state === "running") timer.current = window.setTimeout(tick, 1000); },
      (e: unknown) => {
        if (!current) return;
        if ((e as { status?: number } | null)?.status === 401) setState({ state: "done" });
        else timer.current = window.setTimeout(tick, 1500);
      },
    );
    void tick();
    return () => { current = false; window.clearTimeout(timer.current); };
  }, [job, poll]);
  const moment = backupMoment(backup.created_at);
  const running = !!job && (!state || state.state === "running");
  return (
    <Dialog title="Restore this backup" size="small" open onOpenChange={(o) => { if (!o && !running) onClose(); }}>
      {state?.state === "done" ? (
        <div className="dialog-form restore-confirm">
          <p className="vult-type-ui">Restored. Your Vult is as it was on {moment}.</p>
          <p className="vult-type-ui text-tertiary">Sign in again to continue.</p>
          <div className="vult-confirm-actions"><Button onClick={() => window.location.assign("/")}>Sign in</Button></div>
        </div>
      ) : state?.state === "failed" ? (
        <div className="dialog-form restore-confirm">
          <p className="vult-type-ui">{state.error || "The restore didn’t finish, so Vult was left as it was."}</p>
          <div className="vult-confirm-actions"><Button onClick={onClose}>Close</Button></div>
        </div>
      ) : running ? (
        <div className="dialog-form restore-progress">
          <span className="restore-bar" aria-hidden><span /></span>
          <p className="vult-type-ui">Restoring{state?.step ? `: ${state.step}` : "…"}</p>
          <p className="vult-type-metadata text-tertiary">Keep this page open. Vult may restart on the way.</p>
        </div>
      ) : (
        <form className="dialog-form restore-confirm" onSubmit={(e) => {
          e.preventDefault();
          if (typed !== RESTORE_WORD || busy) return;
          setBusy(true);
          setProblem(null);
          start(backup.id).then((out) => setJob(out.job_id), setProblem).finally(() => setBusy(false));
        }}>
          <p className="vult-type-ui">{backup.lost}</p>
          <Field label={`Type ${RESTORE_WORD} to confirm`}>
            <input name="confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} autoCapitalize="characters" />
          </Field>
          <p className="vult-type-ui restore-passkey"><Fingerprint aria-hidden /> Your passkey confirms it’s you.</p>
          <ErrorNotice error={problem} intent="write" />
          <ConfirmFooter submit confirmLabel="Restore" busy={busy} disabled={typed !== RESTORE_WORD} onCancel={onClose} />
        </form>
      )}
    </Dialog>
  );
}
