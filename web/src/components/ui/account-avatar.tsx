import { Avatar } from "../../design-system/catalog/components/vult-ui/avatar";
import { Minus } from "../../design-system/icons";
import { DATA_PALETTE } from "../../design-system/data-palette";
import {
  ACCOUNT_AVATAR_STANDARD,
  COMPACT_ACCOUNT_AVATAR_STANDARD,
  COUNTERPARTY_AVATAR_STANDARD,
  COUNTERPARTY_FALLBACK_VERSION,
} from "../../design-system/account-avatar-standard";
import type { CSSProperties } from "react";
import { type Institution } from "../../lib/shared-institutions";
import { InstitutionLogo } from "./institution-logo";
import "../../design-system/styles/chart-palette.css";
import "./account-avatar.css";

export function accountInitials(name: string) {
  const words = name.normalize("NFKC").match(/[\p{L}\p{N}]+/gu) || [];
  const letters =
    words.length > 1
      ? words
          .slice(0, 2)
          .map((word) => [...word][0])
          .join("")
      : [...(words[0] || "A")].slice(0, 2).join("");
  return [...letters.toUpperCase()].slice(0, 2).join("");
}

function accountTone(key: string) {
  let hash = 0;
  for (const letter of key)
    hash = (Math.imul(hash, 31) + letter.codePointAt(0)!) >>> 0;
  return hash % DATA_PALETTE.length;
}

type IdentityProps = {
  identityKey: string;
  name: string;
  base: string;
  institution?: Institution;
  pending?: boolean;
};

/** One renderer and one fitting rule for every original and identity role. */
function IdentityAvatar({
  identityKey,
  name,
  base,
  institution,
  pending = false,
  role,
}: IdentityProps & { role: "account" | "compact-account" | "counterparty" }) {
  const standard =
    role === "account"
      ? ACCOUNT_AVATAR_STANDARD
      : role === "compact-account"
        ? COMPACT_ACCOUNT_AVATAR_STANDARD
        : COUNTERPARTY_AVATAR_STANDARD;
  const tone = accountTone(identityKey);
  const unknown = role === "counterparty" && !/[\p{L}\p{N}]/u.test(name);
  return (
    <span
      className="account-avatar"
      data-account-avatar={role !== "counterparty" || undefined}
      data-avatar-density={role === "compact-account" ? "compact" : undefined}
      data-counterparty-avatar={role === "counterparty" || undefined}
      data-avatar-standard={standard.version}
      data-fallback-standard={role === "counterparty" ? COUNTERPARTY_FALLBACK_VERSION : undefined}
      data-avatar-unknown={unknown || undefined}
      data-avatar-pending={pending || undefined}
      data-tone={unknown ? undefined : tone}
      aria-hidden="true"
      style={
        {
          "--account-avatar-size": `${standard.diameter}px`,
          "--account-mark-padding": `${standard.padding}px`,
          "--account-avatar-color": unknown
            ? "var(--color-bg-tertiary)"
            : `var(--color-chart-${DATA_PALETTE[tone]})`,
        } as CSSProperties
      }
    >
      <Avatar
        size={standard.sourceSize}
        className="account-avatar-disk"
        contentClassName="account-avatar-content"
        initials={unknown ? undefined : accountInitials(name)}
        placeholderIcon={unknown ? Minus : undefined}
      />
      {institution && (
        <InstitutionLogo
          base={base}
          institution={institution}
          markSize={standard.markSize}
        />
      )}
    </span>
  );
}

/** Default 40px badge; dense tables may select the fixed compact 24px role. */
export function AccountAvatar({
  accountKey,
  density = "default",
  ...props
}: Omit<IdentityProps, "identityKey"> & {
  accountKey: string;
  density?: "default" | "compact";
}) {
  return (
    <IdentityAvatar
      {...props}
      identityKey={accountKey}
      role={density === "compact" ? "compact-account" : "account"}
    />
  );
}

/** Compact financial tables use the approved 24px role, without page overrides. */
export function CounterpartyAvatar(props: IdentityProps) {
  return <IdentityAvatar {...props} role="counterparty" />;
}
