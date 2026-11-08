/** Exact CSV decoding for retained financial evidence, including quoted newlines.
 * Values remain strings; no floating point money or spreadsheet evaluation. */
export function cashCSV(
  text: string,
  required: readonly string[] = [],
): Record<string, string>[] {
  if (text.length > 32 * 1024 * 1024)
    throw new Error("Bank evidence exceeds the supported size.");
  const rows: string[][] = [];
  let row: string[] = [],
    field = "",
    quoted = false,
    closed = false;
  const endField = () => {
    row.push(field);
    field = "";
    closed = false;
    if (row.length > 64) throw new Error("Invalid bank CSV.");
  };
  const endRow = () => {
    endField();
    if (row.some((value) => value !== "")) rows.push(row);
    row = [];
    if (rows.length > 500001)
      throw new Error("Bank CSV exceeds the supported rows.");
  };
  for (let i = text.charCodeAt(0) === 0xfeff ? 1 : 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          quoted = false;
          closed = true;
        }
      } else field += c;
    } else if (c === '"') {
      if (field || closed) throw new Error("Invalid bank CSV quoting.");
      quoted = true;
    } else if (c === ",") endField();
    else if (c === "\n" || c === "\r") {
      endRow();
      if (c === "\r" && text[i + 1] === "\n") i++;
    } else {
      if (closed) throw new Error("Invalid bank CSV quoting.");
      field += c;
    }
    if (field.length > 131072) throw new Error("Bank CSV field is too large.");
  }
  if (quoted) throw new Error("Bank CSV is truncated.");
  if (field || row.length || closed) endRow();
  const header = rows.shift();
  if (
    !header ||
    new Set(header).size !== header.length ||
    required.some((field) => !header.includes(field))
  )
    throw new Error("Invalid bank CSV header.");
  return rows.map((values) => {
    if (values.length !== header.length)
      throw new Error("Bank CSV is incomplete.");
    return Object.fromEntries(header.map((key, i) => [key, values[i]]));
  });
}
