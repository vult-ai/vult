declare const __VULT_PDF_ASSET_BASE__: string;

export type DocumentPage = {
  width: number;
  height: number;
  text: string;
  paint: (
    canvas: HTMLCanvasElement,
    scale: number,
    signal: AbortSignal,
  ) => Promise<void>;
};
export type RenderDocument = {
  pages: number;
  page: (number: number) => Promise<DocumentPage>;
  destroy: () => void;
};

function resolution(width: number, height: number, scale: number) {
  return Math.min(
    devicePixelRatio || 1,
    2,
    Math.sqrt(8_000_000 / (width * height * scale * scale)),
  );
}

/** Decode passive originals only. PDF actions/annotations never execute. */
export async function openDocument(
  blob: Blob,
  kind: "pdf" | "image",
  signal: AbortSignal,
): Promise<RenderDocument> {
  if (kind === "image") {
    const image = await createImageBitmap(blob);
    if (signal.aborted) {
      image.close();
      throw new DOMException("Aborted", "AbortError");
    }
    return {
      pages: 1,
      page: async () => ({
        width: image.width,
        height: image.height,
        text: "",
        paint: async (canvas, scale) => {
          const ratio = resolution(image.width, image.height, scale);
          canvas.width = Math.max(1, Math.round(image.width * scale * ratio));
          canvas.height = Math.max(1, Math.round(image.height * scale * ratio));
          const context = canvas.getContext("2d");
          if (!context) throw new Error("Canvas unavailable");
          context.drawImage(image, 0, 0, canvas.width, canvas.height);
        },
      }),
      destroy: () => image.close(),
    };
  }
  // The legacy build carries what older browsers lack, such as iOS 18 Safari,
  // which can't loop over a stream while reading a page's text.
  const pdfjs = await import("pdfjs-dist/legacy/build/pdf.mjs");
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  pdfjs.GlobalWorkerOptions.workerSrc = `${__VULT_PDF_ASSET_BASE__}pdf.worker.min.mjs`;
  const data = new Uint8Array(await blob.arrayBuffer());
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  const task = pdfjs.getDocument({
    data,
    cMapUrl: `${__VULT_PDF_ASSET_BASE__}cmaps/`,
    cMapPacked: true,
    standardFontDataUrl: `${__VULT_PDF_ASSET_BASE__}standard_fonts/`,
    wasmUrl: `${__VULT_PDF_ASSET_BASE__}wasm/`,
    iccUrl: `${__VULT_PDF_ASSET_BASE__}iccs/`,
    enableXfa: false,
    verbosity: 0,
  });
  const cancel = () => {
    void task.destroy().catch(() => {});
  };
  signal.addEventListener("abort", cancel, { once: true });
  try {
    const pdf = await task.promise;
    if (signal.aborted) {
      await task.destroy();
      throw new DOMException("Aborted", "AbortError");
    }
    return {
      pages: pdf.numPages,
      page: async (number) => {
        const page = await pdf.getPage(number);
        const size = page.getViewport({ scale: 1 });
        // The page's words are for search and screen readers. A page whose
        // text can't be read still draws.
        let text = "";
        try {
          const content = await page.getTextContent();
          text = content.items
            .map((item) => ("str" in item ? item.str : ""))
            .join(" ")
            .replace(/[\u0000-\u0008\u000B-\u001F\u007F]/g, " ")
            .slice(0, 80_000);
        } catch {
          text = "";
        }
        return {
          width: size.width,
          height: size.height,
          text,
          paint: async (canvas, scale, renderSignal) => {
            const ratio = resolution(size.width, size.height, scale);
            const viewport = page.getViewport({ scale: scale * ratio });
            canvas.width = Math.max(1, Math.floor(viewport.width));
            canvas.height = Math.max(1, Math.floor(viewport.height));
            const render = page.render({ canvas, viewport });
            const stop = () => render.cancel();
            renderSignal.addEventListener("abort", stop, { once: true });
            try {
              await render.promise;
            } finally {
              renderSignal.removeEventListener("abort", stop);
            }
          },
        };
      },
      destroy: () => {
        signal.removeEventListener("abort", cancel);
        void task.destroy().catch(() => {});
      },
    };
  } catch (error) {
    signal.removeEventListener("abort", cancel);
    cancel();
    throw error;
  }
}
