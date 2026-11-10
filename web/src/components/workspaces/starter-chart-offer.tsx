import { useEffect, useRef, useState } from "react";
import type { Book } from "../../lib/api";
import { changeAccounts, readStarterCharts, type StarterChart } from "../../lib/account-changes";
import { ErrorMessage } from "../form";
import { Button } from "../ui/button";
import { ConfirmAction } from "../ui/confirm-action";
import { EmptyState } from "../ui/empty-state";

/** A Book with no accounts can start from a ready chart. Agents otherwise
 * create accounts as they record statements; either way they are ordinary,
 * editable accounts. The chart matching the Book's profile comes first. */
export function StarterChartOffer({ book }: { book: Book }) {
  const [charts, setCharts] = useState<StarterChart[] | null>(null);
  const [readError, setReadError] = useState<unknown>(null);
  const [chosen, setChosen] = useState<StarterChart | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const key = useRef(crypto.randomUUID());
  const trigger = useRef<HTMLElement | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    readStarterCharts(controller.signal).then(
      (out) => setCharts(out.charts),
      (e) => !controller.signal.aborted && setReadError(e),
    );
    return () => controller.abort();
  }, []);
  const ordered = [...(charts || [])].sort((a, b) => Number(b.profile === book.profile) - Number(a.profile === book.profile));
  return (
    <>
      <EmptyState
        scope="page"
        title="No accounts yet"
        detail="Agents create accounts as they record statements. You can also start from a ready chart and change it later."
        action={
          <>
            {ordered.map((chart, index) => (
              <Button
                key={chart.id}
                color={index === 0 ? "primary" : "secondary"}
                onClick={(event) => {
                  trigger.current = event.currentTarget;
                  key.current = crypto.randomUUID();
                  setError(null);
                  setChosen(chart);
                }}
              >
                {`Use ${chart.title} chart`}
              </Button>
            ))}
            <ErrorMessage message={readError} intent="read" resource="accounts" />
          </>
        }
      />
      <ConfirmAction
        open={!!chosen}
        tone="default"
        title={chosen ? `Start with the ${chosen.title} chart?` : ""}
        detail={chosen ? `${chosen.description} It adds ${chosen.accounts.length} accounts to ${book.name}; you can rename, regroup or retire any of them.` : ""}
        confirmLabel="Add accounts"
        busy={busy}
        error={error}
        onConfirm={async () => {
          if (!chosen) return;
          setBusy(true);
          setError(null);
          try {
            await changeAccounts(book.id, { operation: "apply_starter_chart", arguments: { request_key: key.current, template: chosen.id } });
            setChosen(null);
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
        onCancel={() => !busy && setChosen(null)}
        returnTo={trigger.current}
      />
    </>
  );
}
