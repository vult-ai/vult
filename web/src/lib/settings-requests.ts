import { writeHeaders } from "./session";
import { fetchJSON } from "./errors";

type SettingsOperation =
  "settings" | "book-identity" | `book-address?book_id=${string}`;

/** Settings uses the reviewed error transport without changing legacy callers. */
export async function settingsPost<T>(url: SettingsOperation, body: unknown) {
  return fetchJSON<T>(`/api/v1/${url}`, {
    method: "POST",
    headers: await writeHeaders(),
    body: JSON.stringify(body),
  });
}
