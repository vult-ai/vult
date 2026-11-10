import { readBookRoute } from "./book-routing";
export type WorkspaceReadState = {
  failure?: unknown;
  pending: boolean;
  retry: () => Promise<void>;
};
export type Workspace =
  | "dashboard"
  | "transactions"
  | "accounts"
  | "statements"
  | "books"
  | "settings"
  | "documents"
  | "approvals"
  | "reconcile";
export const workspacePath = (page: Workspace) => {
  const route = readBookRoute();
  const tail =
    page === "documents"
      ? "sources"
      : page === "transactions"
        ? "activity"
        : page;
  return route.locator
    ? `/${encodeURIComponent(route.locator)}${tail === "dashboard" ? "" : `/${tail}`}`
    : `/${tail}`;
};
export function readWorkspace(): Workspace {
  const path =
    window.location.pathname === "/" && window.location.hash
      ? ""
      : readBookRoute().page;
  const aliases: Record<string, Workspace> = {
    activity: "transactions",
    transactions: "transactions",
    accounts: "accounts",
    dashboard: "dashboard",
    statements: "statements",
    books: "books",
    settings: "settings",
    sources: "documents",
    documents: "documents",
    approvals: "approvals",
    reconcile: "reconcile",
  };
  if (aliases[path]) return aliases[path];
  const legacy = window.location.hash.slice(1);
  return (
    aliases[legacy] ||
    (legacy === "main-content" ? "transactions" : "dashboard")
  );
}
