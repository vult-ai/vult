import { useState } from "react";
import { ErrorNotice } from "../../src/components/ui/error-notice";
import { responseFailure } from "../../src/lib/errors";
import { parseTransactionJournal } from "../../src/lib/transaction-journal";
import "./error-severity.css";

// The real read validator proves a mismatch in synthetic data. No owner data.
function integrityExample() {
  try {
    parseTransactionJournal(
      "book_id,currency,transaction_id,line_id,account_code,side,amount_minor\npreview,USD,example,line1,cash,debit,1\n",
      { id: "preview", currency: "USD" },
    );
  } catch (error) {
    return error;
  }
}
export function ErrorSeverityPreview() {
  const [error, setError] = useState<unknown>(() => responseFailure(503, {}));
  const [pending, setPending] = useState(false);
  const [critical] = useState(integrityExample);
  const [reviewing, setReviewing] = useState(false);
  return (
    <section className="severity-preview" aria-label="Error severity examples">
      <h2 className="vult-type-section-title">Error emphasis</h2>
      <div className="severity-sample">
        <p className="vult-type-metadata severity-label">Routine</p>
        <ErrorNotice
          error={error}
          intent="read"
          resource="dashboard"
          retained="view"
          pending={pending}
          onRetry={async () => {
            setPending(true);
            setError(undefined);
            await new Promise((resolve) => setTimeout(resolve, 800));
            setPending(false);
          }}
        />
      </div>
      <div className="severity-sample">
        <p className="vult-type-metadata severity-label">Needs attention</p>
        <ErrorNotice
          error={responseFailure(504, {}, "write")}
          intent="write"
          onReview={() => {
            document.getElementById("save-outcome-example")?.scrollIntoView();
          }}
        />
      </div>
      <div className="severity-sample">
        <p className="vult-type-metadata severity-label">
          Critical · verified example
        </p>
        <ErrorNotice
          error={critical}
          intent="read"
          resource="statement"
          onReview={() => setReviewing((value) => !value)}
        />
        {reviewing && (
          <div className="severity-review" role="status">
            <p className="vult-type-ui">
              This example contains an unbalanced entry.
            </p>
            <p className="vult-type-metadata">
              Preview only. No Book was changed.
            </p>
          </div>
        )}
      </div>
    </section>
  );
}
