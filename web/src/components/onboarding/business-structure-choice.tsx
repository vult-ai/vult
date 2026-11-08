import { useEffect, useRef, useState } from "react";
import { Field } from "../form";
import { Button } from "../ui/button";
import type { LegalForm } from "../../lib/legal-identity";
import { OnboardingChoice } from "./onboarding-choice";
import { businessStructureOptions } from "./business-structure-options";

export function BusinessStructureChoice({
  country,
  jurisdiction,
  forms,
  form,
  customForm,
  onChange,
}: {
  country: string;
  jurisdiction: string;
  forms: LegalForm[];
  form: string;
  customForm: string;
  onChange: (choice: { form: string; customForm: string }) => void;
}) {
  const surface = useRef<HTMLDivElement>(null);
  const frame = useRef<number | undefined>(undefined);
  const scope = `${country}/${jurisdiction}`;
  const [view, setView] = useState({ scope, all: false });
  const showAll = view.scope === scope && view.all;
  const choices = businessStructureOptions(
    forms,
    country,
    jurisdiction,
    form,
    customForm,
  );
  const toggle = () => {
    setView({ scope, all: !showAll });
    if (frame.current !== undefined) cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      const input = surface.current?.querySelector<HTMLInputElement>(
        'input[role="combobox"]',
      );
      input?.focus({ preventScroll: true });
      input?.click();
    });
  };
  const actionLabel = showAll
    ? choices.reviewed
      ? "Common structures"
      : "Enter manually"
    : "More structures";
  const baseItems = showAll ? choices.all : choices.common;
  const items = choices.hasMore
    ? [
        ...baseItems.slice(0, -2),
        {
          id: "action:structures",
          label: actionLabel,
          form: "",
          customForm: "",
          onAction: toggle,
        },
        ...baseItems.slice(-2),
      ]
    : baseItems;
  useEffect(
    () => () => {
      if (frame.current !== undefined) cancelAnimationFrame(frame.current);
    },
    [],
  );
  return (
    <div ref={surface}>
      <OnboardingChoice
        label="Business structure"
        name="legal_form"
        placeholder={showAll ? "Search all structures" : "Choose a structure"}
        value={choices.value}
        items={items}
        labelAction={
          choices.hasMore ? (
            <Button color="link" size="sm" onClick={toggle}>
              {actionLabel}
            </Button>
          ) : undefined
        }
        onChange={(value) => {
          const choice = items.find((item) => item.id === value);
          if (choice)
            onChange({ form: choice.form, customForm: choice.customForm });
        }}
      />
      {form === "custom" && choices.value === "custom" && (
        <Field label="Structure name">
          <input
            name="custom_legal_form"
            required
            maxLength={200}
            value={customForm}
            onChange={(event) =>
              onChange({ form: "custom", customForm: event.target.value })
            }
          />
        </Field>
      )}
    </div>
  );
}
