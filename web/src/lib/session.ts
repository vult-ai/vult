import { fetchJSON } from "./errors";

/** The signed-in browser session, as Vult Open reports it at `/auth/session`. */
export type Session = {
  mode: "local" | "server";
  user_id?: string;
  tenants?: string[];
  csrf: string;
  personal_data_version?: string;
  /** The MCP address agents connect to. */
  agent_address?: string;
};

/** Every state-changing request proves the session with this header. */
export const CSRF_HEADER = "X-CSRF-Token";

export function readSession(signal?: AbortSignal) {
  return fetchJSON<Session>("/auth/session", {
    cache: "no-store",
    credentials: "same-origin",
    ...(signal ? { signal } : {}),
  });
}

export function csrfHeaders(session: Pick<Session, "csrf">) {
  return { [CSRF_HEADER]: session.csrf };
}

/** Reads the session afresh so a write never reuses a token from a previous sign-in. */
export async function writeHeaders(json = true, signal?: AbortSignal): Promise<Record<string, string>> {
  const session = await readSession(signal);
  return {
    ...(json ? { "Content-Type": "application/json" } : {}),
    ...csrfHeaders(session),
  };
}
