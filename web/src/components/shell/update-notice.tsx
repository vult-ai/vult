import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, Check, Copy01, XClose } from "../../design-system/icons";
import { APIError, get, post } from "../../lib/api";
import {
  lastUpdateUnfinished,
  updateUnderWay,
  type InstallerReport,
  type UpdateStatus,
} from "../../lib/automatic-updates";
import { Button } from "../ui/button";
import "./update-notice.css";

const dismissedKey = (version: string) => `vult.update.${version}.dismissed`;
const checkEvery = 12 * 60 * 60 * 1000;
/** While an update the owner asked for is under way, look more often. */
const followEvery = 3000;
const updateCommand = "vult update";

/** One check for the workspace, shared by the sidebar and phone drawer. */
export function useUpdateNotice(local: boolean) {
  const [latest, setLatest] = useState<string | null>(null);
  const [installer, setInstaller] = useState<InstallerReport | null>(null);
  const [follow, setFollow] = useState(0);
  const dismissed = useRef<string | null>(null);
  const installerRef = useRef<InstallerReport | null>(null);
  useEffect(() => {
    if (!local) {
      setLatest(null);
      setInstaller(null);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let checked: number | undefined;
    let pending = false;
    const check = async () => {
      if (controller.signal.aborted || pending || document.visibilityState !== "visible") return;
      const following = updateUnderWay(installerRef.current) || installerRef.current?.checking;
      if (!following && checked !== undefined && Date.now() - checked < checkEvery) return;
      checked = Date.now();
      pending = true;
      try {
        const value = await get<UpdateStatus>("update", controller.signal);
        if (controller.signal.aborted) return;
        installerRef.current = value.installer ?? null;
        setInstaller(value.installer ?? null);
        let next = value.available && value.latest ? value.latest : null;
        if (next === dismissed.current) next = null;
        try {
          if (next && localStorage.getItem(dismissedKey(next)) === "1") next = null;
        } catch {
          /* Without storage the notice shows until it is hidden for this visit. */
        }
        setLatest(next);
      } catch (error) {
        if (!controller.signal.aborted && error instanceof APIError && [401, 403].includes(error.status)) {
          setLatest(null);
          setInstaller(null);
        }
      } finally {
        pending = false;
        if (!controller.signal.aborted) {
          clearTimeout(timer);
          timer = setTimeout(() => void check(), updateUnderWay(installerRef.current) || installerRef.current?.checking ? followEvery : checkEvery);
        }
      }
    };
    const revisit = () => void check();
    void check();
    window.addEventListener("focus", revisit);
    document.addEventListener("visibilitychange", revisit);
    return () => {
      controller.abort();
      clearTimeout(timer);
      window.removeEventListener("focus", revisit);
      document.removeEventListener("visibilitychange", revisit);
    };
  }, [local, follow]);
  return {
    latest: local ? latest : null,
    installer: local ? installer : null,
    /** Ask the installer to install the newest release now. */
    async onUpdate(version: string) {
      const result = await post<{ installer: InstallerReport }>("update/install", { version });
      installerRef.current = result.installer;
      setInstaller(result.installer);
      setFollow((n) => n + 1);
    },
    onDismiss() {
      if (!latest) return;
      dismissed.current = latest;
      try {
        localStorage.setItem(dismissedKey(latest), "1");
      } catch {
        /* Hidden for this visit. */
      }
      setLatest(null);
    },
  };
}

/** A newer release: with an installer, an Update button; otherwise the
 * update command. Hiding it lasts until the next release. */
export function UpdateNotice({
  latest,
  installer = null,
  onDismiss,
  onUpdate,
}: {
  latest: string | null;
  installer?: InstallerReport | null;
  onDismiss: () => void;
  onUpdate?: (version: string) => Promise<void>;
}) {
  const [copied, setCopied] = useState(false);
  const [starting, setStarting] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setCopied(false);
    setFailed(false);
  }, [latest]);
  if (!latest) return null;
  const underWay = updateUnderWay(installer);
  const button = installer && onUpdate;
  return (
    <section className="update-notice" aria-label="Update available">
      <header>
        <strong className="vult-type-label">Vult {latest} is available</strong>
        {/* Kept in place while updating, only hidden, so the card doesn't shift. */}
        <button
          type="button"
          className="update-notice-close vult-focus-target"
          aria-label="Hide until the next release"
          onClick={onDismiss}
          data-hidden={underWay || starting || undefined}
          disabled={underWay || starting}
          aria-hidden={underWay || starting || undefined}
          tabIndex={underWay || starting ? -1 : undefined}
        >
          <XClose aria-hidden />
        </button>
      </header>
      {button ? (
        <>
          {!underWay && lastUpdateUnfinished(installer) && <p className="vult-type-metadata">Last try didn’t finish. Nothing changed.</p>}
          {failed && <p className="vult-type-metadata">Couldn’t start. Try again.</p>}
          <Button
            size="sm"
            busy={underWay || starting}
            disabled={underWay || starting}
            onClick={() => {
              setStarting(true);
              setFailed(false);
              onUpdate(latest).then(
                () => setStarting(false),
                () => {
                  setStarting(false);
                  setFailed(true);
                },
              );
            }}
          >
            Update
          </Button>
        </>
      ) : (
        <>
          <p className="vult-type-metadata">Run this in Terminal, or ask the agent to update Vult.</p>
          <div className="update-notice-command">
            <code>{updateCommand}</code>
            <button
              type="button"
              className="update-notice-copy vult-focus-target"
              aria-label={copied ? "Copied" : "Copy command"}
              onClick={() => void navigator.clipboard?.writeText(updateCommand).then(() => setCopied(true), () => undefined)}
            >
              {copied ? <Check aria-hidden /> : <Copy01 aria-hidden />}
            </button>
          </div>
        </>
      )}
      <a className="update-notice-notes vult-type-metadata vult-text-link" href={`https://github.com/vult-ai/vult/releases/tag/v${latest}`} target="_blank" rel="noreferrer">
        What’s new <ArrowUpRight aria-hidden />
      </a>
    </section>
  );
}
