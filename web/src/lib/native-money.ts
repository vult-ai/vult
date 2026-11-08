import { money, nativeMinor, type NativeAmount } from "./api";

/** Shown in place of a native amount whose history the ledger does not hold. */
export const nativeUnavailable = "Not available";
/** The native amount formatted, or the unavailable label. Never zero for a
 * history that is missing. */
export function nativeMoney(value: NativeAmount & { currency: string; scale: number }) {
  const minor = nativeMinor(value);
  return minor === null ? nativeUnavailable : money(minor, value);
}
