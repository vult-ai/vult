# Account and counterparty avatars

Account identities use one circular frame and one fitting rule. The same
component renders every institution and theme; artwork does not define its own
geometry. All dimensions below are CSS pixels.

| Role | Version | Frame diameter | Circular mark area | Radial clearance |
| --- | --- | --- | --- | --- |
| Account | 2 | 40 | 24 | 8 |
| Counterparty and compact account | 1 | 24 | 14 | 5 |

The account frame uses the medium avatar size. The compact role uses the extra
small size, rounding its proportional clearance up to preserve at least the
account role's clearance ratio. Both use a 512-pixel measurement surface for
visible artwork fitting.

`AccountAvatar` and `CounterpartyAvatar` own the background, clipping, padding
and containment. Fit the measured painted extent inside the circular safe
area; a square bounding box alone does not establish circular clearance.
Consumers cannot supply arbitrary geometry or direct institution-image
overrides. Catalog palettes select background and foreground colors without
changing the fitting rule.

Missing, unavailable or unmeasurable originals retain the shared initials or
default fallback. Counterparty fallback policy is versioned separately from
frame geometry. An original is not replaced by a CSS inversion or a separate
theme/size variant.

The constants are in `web/src/design-system/account-avatar-standard.ts`, with
geometry assertions in `web/scripts/check-account-avatar-standard.mjs`. Change
the specification, implementation and assertions together when revising the
standard. Run `npm run assets:check --prefix web`, affected unit tests and the
frontend build, then inspect the rendered circular clearance and contrast.
