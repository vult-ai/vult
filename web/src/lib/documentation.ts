declare const __VULT_DOCUMENTATION_ORIGIN__: string;

/** Pages published at vult.ai/docs. */
export type DocumentationTopic =
  | "get-started"
  | "record-transactions"
  | "understand-numbers"
  | "backup-and-restore"
  | "agent-reference";

/** Public documentation on the website. No book identity or records leave Vult. */
export function documentationURL(
  topic: DocumentationTopic = "get-started",
  section?: string,
) {
  const origin =
    typeof __VULT_DOCUMENTATION_ORIGIN__ === "string"
      ? __VULT_DOCUMENTATION_ORIGIN__
      : "https://vult.ai";
  const url = new URL(`/docs/${topic}`, origin);
  if (section) url.hash = section;
  return url.href;
}
