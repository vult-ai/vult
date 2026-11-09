import { useState } from "react";
import { CheckDone01, ChevronRight, XClose } from "../../design-system/icons";
import { useSidebar } from "./sidebar-context";
import "./onboarding-card.css";

const storageKey = (bookId: string) => `vult.getting-started.${bookId}.dismissed`;

function dismissed(bookId: string) {
  try {
    return localStorage.getItem(storageKey(bookId)) === "1";
  } catch {
    return false;
  }
}

type Step = { title: string; detail: string; open: () => void };

/** The three steps to a Book's first recorded entries, at the foot of the
 * open sidebar. The owner closes it once they no longer need the reminder;
 * that choice is remembered for this Book in this browser. */
export function OnboardingCard({
  bookId,
  onConnectAgent,
  onAddStatement,
  onReview,
}: {
  bookId: string;
  onConnectAgent: () => void;
  onAddStatement: () => void;
  onReview: () => void;
}) {
  const sidebar = useSidebar();
  const [hidden, setHidden] = useState(() => dismissed(bookId));
  const [forBook, setForBook] = useState(bookId);
  if (forBook !== bookId) {
    setForBook(bookId);
    setHidden(dismissed(bookId));
  }
  if (hidden) return null;
  const steps: Step[] = [
    { title: "Connect your agent", detail: "Approve it with your passkey", open: onConnectAgent },
    { title: "Add a statement", detail: "Bank or card, in Sources", open: onAddStatement },
    { title: "Review and approve", detail: "Before anything posts", open: onReview },
  ];
  return (
    <div className="onboarding-slot">
    <section className="onboarding-card" aria-labelledby={`getting-started-${bookId}`}>
      <header className="onboarding-card-header">
        <h2 id={`getting-started-${bookId}`} className="vult-type-label">Getting started</h2>
        <button
          type="button"
          className="onboarding-card-close vult-focus-target"
          aria-label="Hide getting started"
          onClick={() => {
            try {
              localStorage.setItem(storageKey(bookId), "1");
            } catch {
              /* Hidden for now; it returns next visit if storage is unavailable. */
            }
            setHidden(true);
          }}
        >
          <XClose aria-hidden />
        </button>
      </header>
      <ol className="onboarding-card-steps">
        {steps.map((step, i) => (
          <li key={step.title}>
            <button type="button" className="onboarding-card-step vult-focus-target" onClick={step.open}>
              <span className="onboarding-card-number vult-type-metadata" aria-hidden>
                {i + 1}
              </span>
              <span className="onboarding-card-text">
                <span className="vult-type-ui">{step.title}</span>
                <span className="vult-type-metadata">{step.detail}</span>
              </span>
              <ChevronRight className="onboarding-card-chevron" aria-hidden />
            </button>
          </li>
        ))}
      </ol>
    </section>
    {sidebar && (
      // In the collapsed rail the card becomes one icon that opens the sidebar.
      <div className="onboarding-mini-row">
        <button type="button" className="onboarding-mini vult-focus-target" aria-label="Getting started" onClick={sidebar.open}>
          <CheckDone01 aria-hidden />
        </button>
      </div>
    )}
    </div>
  );
}
