import type { InitialBooks } from "./book-state";
import { errorCopy } from "./error-copy";

export type InitialBookResult =
  | (InitialBooks & { error?: never })
  | { catalog?: never; state?: never; source?: never; error: string };
export type InitialBook =
  | { result: InitialBookResult; pending?: never }
  | { result?: never; pending: Promise<InitialBookResult> };
type Application = {
  mount: (initial: InitialBook) => void;
  prepareBook?: () => Promise<void>;
};

/** Read fresh data while the application downloads. A slow read still gets recovery UI. */
export async function startApplication({
  read,
  load,
  slowAfter = 700,
}: {
  read: () => Promise<InitialBooks>;
  load: () => Promise<Application>;
  slowAfter?: number;
}) {
  const pending: Promise<InitialBookResult> = read().then(
    (books) => books,
    (error: unknown) => ({
      error: error instanceof Error ? error.message : errorCopy.unavailable,
    }),
  );
  const app = await load();
  let mounted = false;
  const timer = setTimeout(() => {
    mounted = true;
    app.mount({ pending });
  }, slowAfter);
  try {
    const result = await pending;
    clearTimeout(timer);
    if (!mounted) {
      if (result.state?.book) await app.prepareBook?.();
      app.mount({ result });
    }
  } finally {
    clearTimeout(timer);
  }
}

/** Expose non-financial startup timings for browser qualification, once per document. */
export function markStartupReady(phase: "workspace" | "onboarding" | "table") {
  const key = `vult${phase.charAt(0).toUpperCase()}${phase.slice(1)}ReadyMs`;
  if (!document.documentElement.dataset[key]) {
    document.documentElement.dataset[key] = performance.now().toFixed(1);
  }
}
