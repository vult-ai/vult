import type { ComponentProps } from "react";
import { CounterpartyAvatar } from "./account-avatar";
import type { EntryBrand } from "../../lib/activity-brands";
import { counterpartyAvatarIdentity } from "../../lib/counterparty-identity";
import "./contact-avatar.css";

/** The recorded brand or institution decides the avatar; otherwise the counterparty name. */
export function ContactAvatar({
  brand,
  ...fallback
}: ComponentProps<typeof CounterpartyAvatar> & {
  brand?: EntryBrand;
}) {
  const identity = counterpartyAvatarIdentity({
    brand,
    institution: fallback.institution,
    counterparty: fallback.name,
  });
  return (
    <span className="contact-avatar">
      <CounterpartyAvatar {...fallback} identityKey={identity.key} name={identity.name} />
    </span>
  );
}
