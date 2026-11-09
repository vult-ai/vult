import { APIError } from "./errors";
import { signInWithPasskey } from "./passkey-signin";

/** Writes that need a recent sign-in ask for the passkey once, then retry. */
export async function withRecentSignIn<T>(write: () => Promise<T>): Promise<T> {
  try {
    return await write();
  } catch (e) {
    if (!(e instanceof APIError) || e.status !== 403) throw e;
  }
  try {
    await signInWithPasskey();
  } catch {
    // A cancelled prompt, or an owner who uses an authenticator app: Vult's
    // sign-in page offers both, then returns here to continue.
    window.location.assign("/auth/login");
    throw new Error("Opening sign-in…");
  }
  return write();
}
