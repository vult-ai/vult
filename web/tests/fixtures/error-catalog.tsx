import { errorMessage } from "../../src/lib/errors";
import { useState } from "react";
import { ErrorNotice } from "../../src/components/ui/error-notice";
import { Field } from "../../src/components/form";
import { NativeFieldControl } from "../../src/components/ui/native-field-control";
import { copyExamples, type CopyExample } from "./error-cases";
import "./error-catalog.css";

function CopyRow({ example }: { example: CopyExample }) {
  const [pending, setPending] = useState(false),
    [done, setDone] = useState(false);
  return (
    <div className="copy-example">
      <p className="vult-type-metadata copy-example-label">{example.label}</p>
      {example.field ? (
        <Field label={example.field.label} error={errorMessage(example.error)}>
          <input defaultValue={example.field.value} />
        </Field>
      ) : done ? (
        <p className="vult-type-ui" role="status">
          Loaded.
        </p>
      ) : (
        <ErrorNotice
          error={example.error}
          {...example.context}
          pending={pending}
          onRetry={
            example.retry
              ? async () => {
                  setPending(true);
                  await new Promise((resolve) => setTimeout(resolve, 600));
                  setPending(false);
                  setDone(true);
                }
              : undefined
          }
        />
      )}
    </div>
  );
}
export function ErrorCatalog() {
  const [group, setGroup] = useState("All states");
  const groups = [
    "Loading and access",
    "Saving",
    "Files",
    "Field validation",
  ] as const;
  return (
    <section className="copy-catalog" aria-label="Error copy catalog">
      <header className="copy-catalog-header">
        <h2 className="vult-type-section-title">Other error states</h2>
        <NativeFieldControl>
          <select
            aria-label="Filter error states"
            value={group}
            onChange={(event) => setGroup(event.target.value)}
          >
            <option>All states</option>
            {groups.map((name) => (
              <option key={name}>{name}</option>
            ))}
          </select>
        </NativeFieldControl>
      </header>
      {groups.map((name) => (
        <section
          key={name}
          className="copy-group"
          hidden={group !== "All states" && name !== group}
          aria-label={name}
        >
          <h3 className="vult-type-compact-title">{name}</h3>
          {copyExamples
            .filter((example) => example.group === name)
            .map((example) => (
              <CopyRow key={example.id} example={example} />
            ))}
        </section>
      ))}
    </section>
  );
}
