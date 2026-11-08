/** Bounded presentation values; application records and access state do not belong here. */
export const viewNumber = (value: unknown): value is number =>
  typeof value === "number" &&
  Number.isFinite(value) &&
  value >= 0 &&
  value <= 1_000_000_000;

export const viewStringList = (value: unknown): value is string[] =>
  Array.isArray(value) &&
  value.length <= 10_000 &&
  value.every((item) => typeof item === "string" && item.length <= 2048);

export function validColumnWidths(
  value: unknown,
): value is Record<string, number> {
  return (
    !!value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.entries(value).length <= 100 &&
    Object.entries(value).every(
      ([key, width]) => key.length <= 100 && viewNumber(width),
    )
  );
}
