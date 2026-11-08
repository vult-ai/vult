import { Fragment } from "react";
import type { ImportProvenance } from "../../lib/api";

// Render only inside Record details. These are origin claims, not file links or
// verified accounting facts; the source/evidence viewer owns actual file access.
export function ImportProvenanceDetails({
  value,
}: {
  value?: ImportProvenance;
}) {
  if (!value) return null;
  const fields = [
    ["Provider", value.provider],
    ["Source reference", value.source_reference],
    ["Source SHA-256", value.source_sha256],
    ["Source row", value.source_row],
    ["External reference", value.external_reference],
    ["Import batch", value.batch_id],
    ["Import audit text", value.audit_text],
  ] as const;
  return (
    <section aria-label="Import provenance">
      <h4>Import provenance</h4>
      <dl className="meta">
        {fields
          .filter(([, value]) => value !== undefined && value !== "")
          .map(([label, text]) => (
            <Fragment key={label}>
              <dt>{label}</dt>
              <dd className="preserve-space">{text}</dd>
            </Fragment>
          ))}
      </dl>
    </section>
  );
}
