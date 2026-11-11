import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Loading01 } from "../../design-system/icons";
import { StateLayout } from "../ui/state-layout";
import {
  subscribeConnection,
  connectionReachable,
  setUpdatesInstallHere,
  rememberUpdatesInstallHere,
} from "../../lib/connection";
import { usePersonalAccount } from "./personal-settings-host";
import "./updating-overlay.css";


/**
 * While Vult can't be reached (an update is installing), the page stays as it
 * is underneath and one calm message sits on top. It clears by itself when
 * Vult answers again; reads carry on and nothing typed is lost.
 */
export function UpdatingOverlay({
  showAfter = 800,
  longAfter = 2 * 60 * 1000,
  local,
}: {
  /** Vult on this computer; read from the signed-in session when omitted. */
  local?: boolean;
  /** Don't flash for a blip shorter than this. */
  showAfter?: number;
  /** An update takes about half a minute; after this, say so plainly. */
  longAfter?: number;
} = {}) {
  const session = usePersonalAccount();
  const here = local ?? session?.mode === "local";
  useEffect(() => {
    setUpdatesInstallHere(here);
    // Only a signed-in session says which it is; until then keep what this
    // browser last knew.
    if (session?.mode) rememberUpdatesInstallHere(here);
    return () => setUpdatesInstallHere(false);
  }, [here, session?.mode]);
  const [away, setAway] = useState<number | null>(connectionReachable() ? null : Date.now());
  const [visible, setVisible] = useState(false);
  const [long, setLong] = useState(false);
  useEffect(() => subscribeConnection((reachable) => setAway(reachable ? null : Date.now())), []);
  useEffect(() => {
    if (away === null) {
      setVisible(false);
      setLong(false);
      return;
    }
    const show = setTimeout(() => setVisible(true), showAfter);
    const slow = setTimeout(() => setLong(true), longAfter);
    return () => {
      clearTimeout(show);
      clearTimeout(slow);
    };
  }, [away, showAfter, longAfter]);
  useEffect(() => {
    const root = document.getElementById("root");
    if (!visible || !root) return;
    // Nothing behind the message can be used until Vult is back.
    root.setAttribute("inert", "");
    return () => root.removeAttribute("inert");
  }, [visible]);
  if (!visible) return null;
  return createPortal(
    <div className="updating-overlay vult-studio">
      <StateLayout
        kind="loading"
        scope="page"
        icon={<Loading01 className="vult-state-spinner" />}
        title={long ? "Taking longer than usual" : "Updating Vult"}
        titleId="updating-overlay-title"
        detail={long ? "If this doesn’t clear, restart your Mac." : "Back in a moment."}
        role="status"
      />
    </div>,
    document.body,
  );
}
