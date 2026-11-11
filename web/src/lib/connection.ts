/** Whether this page can reach Vult. When Vult is briefly stopped (an
 * update installs in about half a minute), requests fail to connect: the app
 * shows one calm overlay, reads wait and then carry on, and the overlay
 * clears by itself when Vult answers again. */

type Listener = (reachable: boolean) => void;

const listeners = new Set<Listener>();
let reachable = true;
let waiting: Promise<void> | null = null;
let installsHere = false;

/** Only Vult on this computer updates itself and briefly stops; elsewhere a
 * lost connection is just that, and fails as it always has. */
export function setUpdatesInstallHere(value: boolean) {
  installsHere = value;
}

export function updatesInstallHere() {
  return installsHere;
}

/** Remembered in this browser once it has opened Vult on this computer, so a
 * page that opens while an update installs knows to wait rather than fail. */
const rememberedKey = "vult.updates-install-here";

export function rememberUpdatesInstallHere(value: boolean) {
  try {
    if (value) localStorage.setItem(rememberedKey, "1");
    else localStorage.removeItem(rememberedKey);
  } catch {
    /* Without storage a page opened mid-update shows the usual failure. */
  }
}

export function rememberedUpdatesInstallHere() {
  try {
    return localStorage.getItem(rememberedKey) === "1";
  } catch {
    return false;
  }
}

/** Resolve once Vult answers, however long that takes. */
export async function untilVultAnswers() {
  for (;;) {
    await new Promise((resolve) => setTimeout(resolve, timing.probeEvery));
    if (await probe()) return;
  }
}

const timing = {
  /** How often to look for Vult while it's away. */
  probeEvery: 2000,
  /** Give up waiting for a read after this long; the read then fails as before. */
  waitAtMost: 5 * 60 * 1000,
};

/** Tests shorten the waits. */
export function setConnectionTiming(next: Partial<typeof timing>) {
  Object.assign(timing, next);
}

export function subscribeConnection(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function connectionReachable() {
  return reachable;
}

function set(value: boolean) {
  if (reachable === value) return;
  reachable = value;
  for (const listener of listeners) listener(value);
}

/** Look for Vult with a request that changes nothing. */
async function probe(): Promise<boolean> {
  try {
    const response = await fetch("/auth/session", { cache: "no-store", credentials: "same-origin" });
    // Any answer from Vult itself means it's back, signed in or not.
    return response.status < 500;
  } catch {
    return false;
  }
}

/** Mark Vult unreachable and resolve once it answers again, or after the
 * longest a request should wait. The message stays up, and Vult keeps being
 * looked for, until it really answers. Every caller shares one search. */
export function waitForVult(): Promise<void> {
  set(false);
  if (!waiting) {
    waiting = (async () => {
      for (;;) {
        await new Promise((resolve) => setTimeout(resolve, timing.probeEvery));
        if (await probe()) break;
      }
      set(true);
      waiting = null;
    })();
  }
  return Promise.race([waiting, new Promise<void>((resolve) => setTimeout(resolve, timing.waitAtMost))]);
}
