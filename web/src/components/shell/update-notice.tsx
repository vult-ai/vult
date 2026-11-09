import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, Check, Copy01, XClose } from "../../design-system/icons";
import { APIError, get } from "../../lib/api";
import "./update-notice.css";

type UpdateStatus = { current: string; latest?: string; available: boolean };

const dismissedKey = (version: string) => `vult.update.${version}.dismissed`;
const checkEvery = 12 * 60 * 60 * 1000;
const updateCommand = "vult update";

/** One check for the workspace, shared by the sidebar and phone drawer. */
export function useUpdateNotice(local: boolean) {
  const [latest, setLatest] = useState<string | null>(null);
  const dismissed = useRef<string | null>(null);
  useEffect(() => {
    if (!local) {
      setLatest(null);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let checked: number | undefined;
    let pending = false;
    const check = async () => {
      if (controller.signal.aborted || pending || document.visibilityState !== "visible") return;
      if (checked !== undefined && Date.now() - checked < checkEvery) return;
      checked = Date.now();
      pending = true;
      try {
        const value = await get<UpdateStatus>("update", controller.signal);
        if (controller.signal.aborted) return;
        let next = value.available && value.latest ? value.latest : null;
        if (next === dismissed.current) next = null;
        try {
          if (next && localStorage.getItem(dismissedKey(next)) === "1") next = null;
        } catch {
          /* Without storage the notice shows until it is hidden for this visit. */
        }
        setLatest(next);
      } catch (error) {
        if (!controller.signal.aborted && error instanceof APIError && [401, 403].includes(error.status)) setLatest(null);
      } finally {
        pending = false;
        if (!controller.signal.aborted) {
          clearTimeout(timer);
          timer = setTimeout(() => void check(), checkEvery);
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
  }, [local]);
  return {
    latest: local ? latest : null,
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

/** A newer release and its update command. Hiding it lasts until the next release. */
export function UpdateNotice({ latest, onDismiss }: { latest: string | null; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => setCopied(false), [latest]);
  if (!latest) return null;
  return (
    <section className="update-notice" aria-label="Update available">
      <header>
        <strong className="vult-type-label">Vult {latest} is available</strong>
        <button
          type="button"
          className="update-notice-close vult-focus-target"
          aria-label="Hide until the next release"
          onClick={onDismiss}
        >
          <XClose aria-hidden />
        </button>
      </header>
      <p className="vult-type-metadata">Run this in Terminal, or ask your agent to update Vult.</p>
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
      <a className="update-notice-notes vult-type-metadata vult-text-link" href={`https://github.com/vult-ai/vult/releases/tag/v${latest}`} target="_blank" rel="noreferrer">
        What’s new <ArrowUpRight aria-hidden />
      </a>
    </section>
  );
}
