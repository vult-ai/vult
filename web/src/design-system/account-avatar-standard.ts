/** Vult account identity standard v2. See docs/design/avatars.md.
 * 40px frame / 24px circular safe area / at least 8px radial clear space.
 * Change it only by publishing a new version of the standard, never through a
 * brand, theme, viewport or page-specific adjustment.
 */
export const ACCOUNT_AVATAR_STANDARD = Object.freeze({
  version: 2,
  sourceSize: "md" as const,
  diameter: 40,
  markSize: 24,
  padding: 8,
  markShape: "circle" as const,
  fit: "painted-radius" as const,
  measurementSize: 512,
});

/** Compact counterparty role: the same minimum clearance proportion.
 * Account geometry stays v2; consumers cannot supply arbitrary sizes or insets.
 */
const counterpartyDiameter = 24;
const counterpartyPadding = Math.ceil(
  (ACCOUNT_AVATAR_STANDARD.padding * counterpartyDiameter) /
    ACCOUNT_AVATAR_STANDARD.diameter,
);
export const COUNTERPARTY_AVATAR_STANDARD = Object.freeze({
  ...ACCOUNT_AVATAR_STANDARD,
  version: 1,
  sourceSize: "xs" as const,
  diameter: counterpartyDiameter,
  markSize: counterpartyDiameter - 2 * counterpartyPadding,
  padding: counterpartyPadding,
});

/** Dense account tables reuse the compact identity geometry. */
export const COMPACT_ACCOUNT_AVATAR_STANDARD = COUNTERPARTY_AVATAR_STANDARD;

/** Identity fallback policy is versioned separately from fixed frame geometry. */
export const COUNTERPARTY_FALLBACK_VERSION = 2;
