import { ErrorCatalog } from "./error-catalog";
import { ErrorSeverityPreview } from "./error-severity";
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { ErrorNotice } from "../../src/components/ui/error-notice";
import { Field } from "../../src/components/form";
import { Button } from "../../src/components/ui/button";
import { responseFailure } from "../../src/lib/errors";
import "../../src/style.css";

// Synthetic-only: no owner records, requests, browser storage or real postings.
function FeedbackFixture() {
  const [error, setError] = useState<unknown>(
    responseFailure(
      503,
      { error: "endpoint not found SQLSTATE /private/secret" },
      "read",
    ),
  );
  const [pending, setPending] = useState(false);
  const [theme, setTheme] = useState("light");
  const [draft, setDraft] = useState("Office supplies");
  const [receipt, setReceipt] = useState("");
  const [saved, setSaved] = useState(false);
  return (
    <main
      className="vult-studio"
      style={{
        background: "var(--vult-studio-canvas)",
        maxWidth: 800,
        margin: "32px auto",
        padding: 16,
        display: "grid",
        gap: 16,
      }}
    >
      <header style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
        <Button
          color="secondary"
          onClick={() => {
            const next = theme === "light" ? "dark" : "light";
            setTheme(next);
            document.documentElement.classList.toggle("dark-mode", next === "dark");
          }}
        >
          Toggle theme
        </Button>
        <Button
          color="secondary"
          onClick={() => setError(responseFailure(503, {}, "read"))}
        >
          Show read failure
        </Button>
      </header>
      <p className="vult-type-metadata">
        Preview only. Your Books won’t change.
      </p>
      <ErrorSeverityPreview />
      <section
        style={{
          padding: 16,
          borderRadius: 16,
          background: "var(--color-bg-secondary)",
          display: "grid",
          gap: 16,
        }}
      >
        <h1 className="vult-type-section">Your dashboard</h1>
          <ErrorNotice
          error={error}
          intent="read"
          resource="dashboard"
          retained="view"
          pending={pending}
          onRetry={async () => {
            setPending(true);
            setError(undefined);
            await new Promise((resolve) => setTimeout(resolve, 900));
            setPending(false);
          }}
        />
      </section>
      <section
        id="save-outcome-example"
        style={{
          padding: 16,
          borderRadius: 16,
          background: "var(--color-bg-primary)",
          display: "grid",
          gap: 12,
        }}
      >
        <Field label="Description">
          <input
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
        </Field>
        <Field label="Receipt">
          <input
            type="file"
            onChange={(event) =>
              setReceipt(event.target.files?.[0]?.name ?? "")
            }
          />
        </Field>
        {receipt && <p className="vult-type-metadata">{receipt}</p>}
        <ErrorNotice
          error={saved ? undefined : responseFailure(504, {}, "write")}
          intent="write"
          retained="form"
        />
        <Button color="secondary" onClick={() => setSaved(true)}>
          Show saved state
        </Button>
      </section>
      <ErrorCatalog />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<FeedbackFixture />);
