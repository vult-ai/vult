import { useEffect, useId, useRef, useState } from "react";
import { Check, Copy01 } from "../../design-system/icons";
import { Button } from "../ui/button";
import "./first-run.css";

const steps = [
  {
    title: "Create a Book",
    detail: "Give it a name and its currency, with the number of decimal places (2 for dollars or euros). Each Book keeps one currency.",
    command: 'vult book create --name "Personal" --currency USD --scale 2',
  },
  {
    title: "Connect your agent",
    detail:
      "Use the Book ID the first command printed. Add the MCP entry this one prints to your agent’s settings. You approve each agent session.",
    command: 'vult connection create --name "My agent" --book BOOK_ID --access write',
  },
] as const;

/** Shown while no Book exists. Owners create Books and connections in the terminal;
 * this page notices the first Book on its own. */
export function FirstRun({
  onCheck,
  checking,
}: {
  onCheck: () => void;
  checking: boolean;
}) {
  const heading = useId();
  // Only a check the owner asked for shows as busy; the automatic ones stay quiet.
  const [asked, setAsked] = useState(false);
  useEffect(() => { if (!checking) setAsked(false); }, [checking]);
  // Look for the new Book every few seconds while the page is visible.
  // The latest onCheck is read from a ref, so a new callback each render keeps one timer.
  const check = useRef(onCheck);
  check.current = onCheck;
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") check.current();
    }, 5000);
    return () => window.clearInterval(id);
  }, []);
  return (
    <section className="first-run" aria-labelledby={heading}>
      <h1 id={heading}>Start your first Book</h1>
      <p className="first-run-intro">
        Your agent records the Book. You review and approve it here. Run these
        two commands in your terminal; this page opens your Book as soon as it
        exists.
      </p>
      <ol className="first-run-steps">
        {steps.map((step) => (
          <li key={step.title}>
            <h2>{step.title}</h2>
            <p>{step.detail}</p>
            <div className="first-run-command">
              <pre>
                <code>{step.command}</code>
              </pre>
              <CopyCommand text={step.command} />
            </div>
          </li>
        ))}
      </ol>
      <p className="first-run-note">
        On a Vult server, your administrator creates Books, and your agent
        connects by signing in.
      </p>
      <div className="onboarding-actions">
        <Button color="secondary" onClick={() => { setAsked(true); onCheck(); }} busy={asked && checking}>
          Check now
        </Button>
      </div>
    </section>
  );
}

function CopyCommand({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(id);
  }, [copied]);
  return (
    <Button
      color="tertiary"
      size="sm"
      className="first-run-copy"
      aria-label={copied ? "Copied" : "Copy command"}
      title={copied ? "Copied" : "Copy command"}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => setCopied(true), () => undefined);
      }}
    >
      {copied ? <Check aria-hidden="true" /> : <Copy01 aria-hidden="true" />}
    </Button>
  );
}
