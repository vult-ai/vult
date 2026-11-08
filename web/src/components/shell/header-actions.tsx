import { createContext, useContext, type ReactNode } from "react";
import { createPortal } from "react-dom";

const HeaderActionsContext = createContext<HTMLElement | null>(null);
export const HeaderActionsProvider = HeaderActionsContext.Provider;

/** The page keeps ownership of its controls while the shell owns placement. */
export function HeaderActions({
  children,
  active = true,
  fallback,
}: {
  children: ReactNode;
  active?: boolean;
  fallback?: ReactNode;
}) {
  const target = useContext(HeaderActionsContext);
  if (!active) return null;
  return target ? createPortal(children, target) : (fallback ?? null);
}
