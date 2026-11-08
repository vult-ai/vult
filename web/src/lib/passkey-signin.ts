/** Signs in again with the owner's passkey from inside the app, for the steps
 * that need a recent sign-in (creating a Book, letting an agent in). It uses
 * the same routes as Vult's sign-in page. Throws `SignInCancelled` when the
 * owner dismisses the prompt, and `SignInUnavailable` when this browser or
 * account can't sign in here, so the caller can send them to the sign-in page. */

export class SignInCancelled extends Error {}
export class SignInUnavailable extends Error {}

const decode = (value: string) =>
  Uint8Array.from(atob(value.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
const encode = (value: ArrayBuffer) =>
  btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

type Descriptor = { id: string; type: string; transports?: AuthenticatorTransport[] };
type LoginOptions = { publicKey: Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> & { challenge: string; allowCredentials?: Descriptor[] } };

async function post(path: string, csrf: string, body: unknown) {
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    redirect: "error",
    cache: "no-store",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify(body),
  });
  if (!response.ok || !response.headers.get("Content-Type")?.includes("application/json")) throw new SignInUnavailable("Sign-in is unavailable.");
  return response.json() as Promise<unknown>;
}

export async function signInWithPasskey(): Promise<void> {
  if (!window.isSecureContext || !navigator.credentials || typeof PublicKeyCredential === "undefined") throw new SignInUnavailable("This browser can’t use a passkey here.");
  // The sign-in page issues the one-time token these requests prove.
  const page = await fetch("/auth/login", { credentials: "same-origin", cache: "no-store", redirect: "error" }).catch(() => null);
  if (!page?.ok) throw new SignInUnavailable("Sign-in is unavailable.");
  const csrf = new DOMParser().parseFromString(await page.text(), "text/html").querySelector('meta[name="vult-login-csrf"]')?.getAttribute("content");
  if (!csrf) throw new SignInUnavailable("Sign-in is unavailable.");
  const options = (await post("/auth/passkeys/login/options", csrf, {})) as LoginOptions;
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...options.publicKey,
    challenge: decode(options.publicKey.challenge),
    allowCredentials: options.publicKey.allowCredentials?.map((c) => ({ ...c, type: "public-key" as const, id: decode(c.id) })),
  };
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.get({ publicKey });
  } catch (e) {
    if (e instanceof DOMException && e.name === "NotAllowedError") throw new SignInCancelled("Sign-in was cancelled.");
    throw new SignInUnavailable("This passkey can’t be used here.");
  }
  if (!(credential instanceof PublicKeyCredential)) throw new SignInCancelled("Sign-in was cancelled.");
  const response = credential.response as AuthenticatorAssertionResponse;
  const json =
    typeof (credential as PublicKeyCredential & { toJSON?: () => unknown }).toJSON === "function"
      ? (credential as PublicKeyCredential & { toJSON: () => unknown }).toJSON()
      : {
          id: credential.id,
          rawId: encode(credential.rawId),
          type: credential.type,
          authenticatorAttachment: credential.authenticatorAttachment,
          response: {
            clientDataJSON: encode(response.clientDataJSON),
            authenticatorData: encode(response.authenticatorData),
            signature: encode(response.signature),
            ...(response.userHandle ? { userHandle: encode(response.userHandle) } : {}),
          },
          clientExtensionResults: credential.getClientExtensionResults(),
        };
  const result = (await post("/auth/passkeys/login/verify", csrf, { credential: json })) as { signed_in?: boolean };
  if (result.signed_in !== true) throw new SignInUnavailable("Sign-in wasn’t confirmed.");
}
