import { createContext, useContext } from "react";

/** Lets something in the collapsed rail open the sidebar. */
export const SidebarContext = createContext<{ open: () => void } | null>(null);

export function useSidebar() {
  return useContext(SidebarContext);
}
