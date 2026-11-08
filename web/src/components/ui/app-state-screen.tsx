import type { ReactNode } from "react";
import "./state.css";

/**
 * The whole-window frame for opening, failing to open, or not finding a Book:
 * the plain application canvas with one centred state. Onboarding keeps its
 * own layout; these screens are not onboarding.
 */
export function AppStateScreen({ children }: { children: ReactNode }) {
  return (
    <main id="main-content" tabIndex={-1} className="vult-studio app-state-screen">
      {children}
    </main>
  );
}
