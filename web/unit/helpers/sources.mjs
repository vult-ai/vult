import { React, loadComponent } from "./dom.mjs";
export const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic Book",
  currency: "USD",
  scale: 2,
};
export const otherBook = {
  ...book,
  id: "22222222-2222-4222-8222-222222222222",
  name: "Other Book",
};
export const source = (i = 0, fields = {}) => ({
  id: `aaaaaaaa-aaaa-4aaa-8aaa-${String(i + 1).padStart(12, "0")}`,
  scope: "inbox",
  blob_id: `blob-${i}`,
  original_filename: `Raw ${i + 1}.pdf`,
  canonical_name: `Statement ${i + 1}.pdf`,
  content_type: "application/pdf",
  size: 20480,
  sha256: "a".repeat(64),
  metadata: {
    kind: "statement",
    issuer: "Synthetic issuer",
    document_date: "",
    period_start: "",
    period_end: "",
    reference: "",
    currency: "",
  },
  revision: 1,
  created_at: "2026-10-26T12:00:00Z",
  updated_at: "2026-10-26T12:00:00Z",
  ...fields,
});
export const files = Array.from({ length: 27 }, (_, i) => source(i));
export const response = (data) => ({ ok: true, json: async () => data });
export const failure = (error, status = 503) => ({
  ok: false,
  status,
  json: async () => ({ error }),
});
export const page = (sources, next_cursor = "", total = sources.length) =>
  response({ sources, next_cursor, has_more: !!next_cursor, total });
export const originalResponse = () => ({
  ok: true,
  blob: async () =>
    new Blob(["%PDF-1.7 synthetic test bytes"], { type: "application/pdf" }),
});
export const lifecycle = (id, fields = {}) => ({
  source_id: id,
  claims: [],
  artifacts: [],
  relationships: [],
  outcomes: [],
  coverage: {
    basis: "not_assessed",
    message: "Accounting coverage has not been assessed.",
  },
  has_more: { artifacts: false, relationships: false, outcomes: false },
  ...fields,
});
export const preview = () =>
  document.querySelector('[data-testid="document-preview"]');
export const table = () =>
  document.querySelector('[data-testid="book-documents"]');
export const trigger = (i = 0) =>
  document.querySelector(`button[aria-label="Preview Statement ${i + 1}.pdf"]`);
export const click = async (element) => {
  if (!element) throw new Error("Expected a reachable control");
  await React.act(async () => element.click());
};
export const button = (label) =>
  [...document.querySelectorAll("button")].find(
    (element) => element.textContent.trim() === label,
  );
export const close = () =>
  click(document.querySelector('[aria-label="Clear source selection"]'));
export const frame = (window) =>
  React.act(
    async () => new Promise((done) => window.requestAnimationFrame(done)),
  );
export const input = async (window, element, value) =>
  React.act(async () => {
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(element, value);
    element.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
export const selectScope = async (window, value) =>
  React.act(async () => {
    const field = document.querySelector(
      'select[aria-label="Source location"]',
    );
    field.value = value;
    field.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
export const waitForSearch = () =>
  React.act(async () => new Promise((done) => setTimeout(done, 210)));
export async function fixture(render, options = {}) {
  const { Documents } = await loadComponent(
    "src/components/book/documents.tsx",
  );
  const savedFetch = globalThis.fetch,
    savedCreate = URL.createObjectURL,
    savedRevoke = URL.revokeObjectURL;
  const reads = [],
    blobs = [],
    revoked = [];
  let objects = options.files || files;
  URL.createObjectURL = (blob) => {
    blobs.push(blob);
    return `blob:synthetic-${blobs.length}`;
  };
  URL.revokeObjectURL = (url) => revoked.push(url);
  globalThis.fetch = async (url, init) => {
    const parsed = new URL(url, "http://localhost");
    reads.push({ url: parsed, init });
    if (parsed.pathname === "/auth/session")
      return response({ csrf: "synthetic-csrf" });
    if (parsed.pathname === "/api/v1/sources" && init?.method === "POST")
      return options.store
        ? options.store(parsed, init)
        : response({ source: source(50), reused: false });
    if (parsed.pathname === "/api/v1/sources")
      return options.list ? options.list(parsed, init) : page(objects);
    const match = /^\/api\/v1\/sources\/([^/]+)(.*)$/.exec(parsed.pathname);
    if (match) {
      const id = decodeURIComponent(match[1]);
      if (match[2] === "/content")
        return options.original
          ? options.original(parsed, init)
          : originalResponse();
      if (match[2] === "/lifecycle")
        return options.lifecycle
          ? options.lifecycle(id, init)
          : response(lifecycle(id));
      if (match[2] === "/history")
        return options.history
          ? options.history(id, parsed, init)
          : response({ revisions: [], has_more: false });
      if (match[2].startsWith("/artifacts/"))
        return options.artifact
          ? options.artifact(id, match[2].split("/").at(-1), init)
          : response({ payload: {} });
      if (!match[2] && init?.method === "PATCH") {
        if (options.patch)
          return options.patch(id, JSON.parse(init.body), init);
        const request = JSON.parse(init.body),
          original = objects.find((item) => item.id === id);
        const next = {
          ...original,
          canonical_name: request.canonical_name ?? original.canonical_name,
          metadata: request.metadata,
          revision: original.revision + 1,
        };
        objects = objects.map((item) => (item.id === id ? next : item));
        return response(next);
      }
      if (!match[2])
        return options.read
          ? options.read(id, init)
          : response(objects.find((item) => item.id === id));
    }
    throw new Error(`Unexpected request: ${parsed.pathname}`);
  };
  function Workbench(props) {
    const [query, setQuery] = React.useState("");
    // Sources searches in its own toolbar field (docs/design/search.md).
    return React.createElement(Documents, { book, ...props, query, onQueryChange: setQuery });
  }
  const mount = (props = {}) =>
    render(
      React.createElement(Workbench, {
        key: (props.book || book).id,
        ...props,
      }),
    );
  await mount();
  return {
    mount,
    reads,
    blobs,
    revoked,
    originals: () =>
      reads.filter((read) => read.url.pathname.endsWith("/content")),
    lists: () =>
      reads.filter(
        (read) => read.url.pathname === "/api/v1/sources" && !read.init?.method,
      ),
    posts: () => reads.filter((read) => read.init?.method === "POST"),
    patches: () => reads.filter((read) => read.init?.method === "PATCH"),
    restore() {
      globalThis.fetch = savedFetch;
      URL.createObjectURL = savedCreate;
      URL.revokeObjectURL = savedRevoke;
    },
  };
}
