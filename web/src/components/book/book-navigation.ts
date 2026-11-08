import { BookOpen01, Stamp, Dataflow03, Folder, SquaresFour, Wallet01, File06 } from "../../design-system/icons";
import type { NavigationItem } from "../shell/navigation";
import {
  readWorkspace,
  workspacePath,
  type Workspace,
} from "../../lib/workspace";
export { readWorkspace, workspacePath, type Workspace };
export const documentWorkspace: NavigationItem = {
  id: "documents",
  label: "Sources",
  icon: Folder,
  href: "/sources",
};
/** What needs the owner comes first: the overview and anything waiting for
 * approval. A gap then sets off the records, most visited first. */
export const bookNavigation: readonly NavigationItem[] = [
  { id: "dashboard", label: "Dashboard", icon: SquaresFour, href: "/dashboard" },
  { id: "approvals", label: "Approvals", icon: Stamp, href: "/approvals" },
  { id: "transactions", label: "Activity", icon: Dataflow03, href: "/activity", groupStart: true },
  { id: "accounts", label: "Accounts", icon: Wallet01, href: "/accounts" },
  { id: "statements", label: "Statements", icon: File06, href: "/statements" },
  documentWorkspace,
];
export const booksWorkspace: NavigationItem = {
  id: "books",
  label: "Books",
  icon: BookOpen01,
  href: "/books",
};
