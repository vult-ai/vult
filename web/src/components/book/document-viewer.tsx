import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ChevronLeft,
  ChevronRight,
  Expand01,
  ZoomIn,
  ZoomOut,
} from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { Dialog } from "../ui/dialog";
import { errorMessage, responseFailure } from "../../lib/errors";
import { readDocument } from "../../lib/read-document";
import {
  openDocument,
  type DocumentPage,
  type RenderDocument,
} from "../../lib/document-engine";
import "./document-viewer.css";
import { LoadingState } from "../ui/loading-state";
import { FailureState } from "../ui/failure-state";

type Fit = "page" | "width" | "recall" | number;
type Position = { top: number; left: number };
const minimumZoom = 0.1,
  maximumZoom = 4;

/** One file read/decoder owns the embedded and expanded reading surfaces. */
export function DocumentViewer({
  src,
  name,
  kind,
  presentation = "reader",
}: {
  src: string;
  name: string;
  kind: "pdf" | "image" | "unsupported";
  presentation?: "reader" | "recall" | "preview";
}) {
  const [attempt, setAttempt] = useState(0);
  const [model, setModel] = useState<RenderDocument | null>(null);
  const [error, setError] = useState("");
  const [reading, setReading] = useState(false);
  const [page, setPage] = useState(1);
  const [expanded, setExpanded] = useState(false);
  const returnTo = useRef<HTMLElement | null>(null);
  const embeddedPosition = useRef<Position>({ top: 0, left: 0 });
  const expandedPosition = useRef<Position>({ top: 0, left: 0 });
  const [inlineFit, setInlineFit] = useState<Fit>(
    presentation === "recall" ? "recall" : "page",
  );
  const [readingFit, setReadingFit] = useState<Fit>("width");
  useEffect(() => {
    if (kind === "unsupported") return;
    const controller = new AbortController();
    let current = true,
      decoded: RenderDocument | undefined;
    setModel(null);
    setReading(true);
    setError("");
    void (async () => {
      try {
        const response = await readDocument(src, controller.signal);
        if (!response.ok)
          throw responseFailure(
            response.status,
            await response.json().catch(() => null),
            "read",
          );
        const blob = await response.blob();
        if (!current) return;
        if (!blob.size) throw new Error("Empty document");
        try {
          decoded = await openDocument(blob, kind, controller.signal);
        } catch (failure) {
          if (controller.signal.aborted) return;
          throw new Error(
            "This document could not be previewed. Open the original or try again.",
            { cause: failure },
          );
        }
        if (current) setModel(decoded);
        else decoded.destroy();
      } catch (failure) {
        if (current) setError(errorMessage(failure));
      } finally {
        if (current) setReading(false);
      }
    })();
    return () => {
      current = false;
      controller.abort();
      decoded?.destroy();
    };
  }, [src, kind, attempt]);

  const expand = (source: HTMLElement | null) => {
    returnTo.current = source;
    setExpanded(true);
  };
  const surface = (full: boolean) => (
    <ViewerSurface
      key={full ? "expanded" : "inline"}
      model={model}
      error={error}
      reading={reading}
      name={name}
      page={page}
      setPage={setPage}
      fit={full ? readingFit : inlineFit}
      setFit={full ? setReadingFit : setInlineFit}
      position={full ? expandedPosition : embeddedPosition}
      expanded={full}
      recall={!full && presentation !== "reader"}
      active={full ? expanded : !expanded}
      onExpand={expand}
      onRetry={() => setAttempt((n) => n + 1)}
    />
  );
  if (kind === "unsupported")
    return (
      <div className="document-preview-placeholder">
        <p>A preview isn’t available for this file type.</p>
        <p>Open the original file to view it.</p>
      </div>
    );
  return (
    <>
      <div
        className={`document-preview-viewer document-viewer document-preview-${kind}`}
        data-document-url={src}
        data-presentation={presentation}
        aria-label={`Preview of ${name}`}
      >
        {surface(false)}
      </div>
      <Dialog
        title={name}
        description="Document reading view. Use the controls to change pages, fit and zoom."
        open={expanded}
        onOpenChange={setExpanded}
        returnTo={returnTo.current}
        size="viewer"
        className="document-viewer-dialog"
      >
        {surface(true)}
      </Dialog>
    </>
  );
}

function ViewerSurface({
  model,
  name,
  page,
  setPage,
  fit,
  setFit,
  expanded,
  recall,
  active,
  onExpand,
  error,
  reading,
  onRetry,
  position,
}: {
  model: RenderDocument | null;
  name: string;
  page: number;
  setPage: (page: number) => void;
  fit: Fit;
  setFit: (fit: Fit) => void;
  expanded: boolean;
  recall: boolean;
  active: boolean;
  onExpand: (source: HTMLElement | null) => void;
  error: string;
  reading: boolean;
  onRetry: () => void;
  position: React.RefObject<Position>;
}) {
  const viewport = useRef<HTMLDivElement>(null),
    canvas = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [sheet, setSheet] = useState<{
    page: number;
    value: DocumentPage;
  } | null>(null);
  const [painted, setPainted] = useState(false),
    [renderError, setRenderError] = useState(false);
  const restored = useRef(false);
  const oldScale = useRef(0),
    oldPage = useRef(page);
  const current = sheet?.page === page ? sheet.value : null;
  const availableWidth = Math.max(1, size.width - 24),
    availableHeight = Math.max(1, size.height - 24);
  const scale = current
    ? typeof fit === "number"
      ? fit
      : fit === "recall"
        ? Math.min(availableWidth, 400) / current.width
        : fit === "width"
          ? availableWidth / current.width
          : Math.min(
              availableWidth / current.width,
              availableHeight / current.height,
            )
    : 1;
  const clamped = Math.min(
    maximumZoom,
    fit === "page" ? scale : Math.max(minimumZoom, scale),
  );

  useLayoutEffect(() => {
    const node = viewport.current;
    if (!node) return;
    const measure = () =>
      setSize((previous) => {
        const next = { width: node.clientWidth, height: node.clientHeight };
        return previous.width === next.width && previous.height === next.height
          ? previous
          : next;
      });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [position]);
  useEffect(() => {
    if (!model) return;
    let current = true;
    setPainted(false);
    setRenderError(false);
    void model.page(page).then(
      (value) => {
        if (current) setSheet({ page, value });
      },
      () => {
        if (current) setRenderError(true);
      },
    );
    return () => {
      current = false;
    };
  }, [model, page]);
  useEffect(() => {
    if (!current || !size.width || !size.height || !active) return;
    const controller = new AbortController();
    const target = canvas.current;
    if (!target) return;
    setRenderError(false);
    // Paint offscreen: interrupted zoom never clears the visible page.
    const buffer = document.createElement("canvas");
    void current
      .paint(buffer, clamped, controller.signal)
      .then(() => {
        if (controller.signal.aborted) return;
        target.width = buffer.width;
        target.height = buffer.height;
        const context = target.getContext("2d");
        if (!context) throw new Error("Canvas unavailable");
        context.drawImage(buffer, 0, 0);
        setPainted(true);
      })
      .catch(() => {
        if (!controller.signal.aborted) setRenderError(true);
      });
    return () => controller.abort();
  }, [current, clamped, size.width, size.height, active]);
  useLayoutEffect(() => {
    const node = viewport.current;
    if (!node || !current || !size.width || !size.height) return;
    if (!restored.current) {
      node.scrollTop = position.current.top;
      node.scrollLeft = position.current.left;
      restored.current = true;
    } else if (oldPage.current !== page) {
      node.scrollTop = 0;
      node.scrollLeft = 0;
    } else if (
      oldScale.current &&
      oldScale.current !== clamped &&
      typeof fit === "number"
    ) {
      const factor = clamped / oldScale.current;
      node.scrollLeft =
        (node.scrollLeft + node.clientWidth / 2) * factor -
        node.clientWidth / 2;
      node.scrollTop =
        (node.scrollTop + node.clientHeight / 2) * factor -
        node.clientHeight / 2;
    }
    oldScale.current = clamped;
    oldPage.current = page;
  }, [clamped, current, page, fit, size.width, size.height]);
  const failed =
    error ||
    (renderError
      ? "This document could not be previewed. Open the original or try again."
      : "");
  const ready = !!current && !!model && !failed;
  const changePage = (next: number) => {
    if (model && next >= 1 && next <= model.pages) setPage(next);
  };
  return (
    <div
      className={`document-viewer-surface ${expanded ? "document-viewer-expanded" : ""} ${recall ? "document-viewer-recall" : ""}`}
      aria-busy={!failed && !painted}
    >
      <div
        ref={viewport}
        className="document-viewer-viewport vult-focus-target vult-focus-contained"
        tabIndex={ready ? 0 : -1}
        aria-label={`Document page ${page}`}
        role="region"
        onScroll={(event) => {
          position.current = {
            top: event.currentTarget.scrollTop,
            left: event.currentTarget.scrollLeft,
          };
        }}
        onKeyDown={(event) => {
          if (event.target !== event.currentTarget) return;
          if (event.key === "+" || event.key === "=") {
            event.preventDefault();
            setFit(Math.min(maximumZoom, clamped * 1.25));
          }
          if (event.key === "-") {
            event.preventDefault();
            setFit(Math.max(minimumZoom, clamped / 1.25));
          }
          if (event.key === "ArrowRight") {
            event.preventDefault();
            changePage(page + 1);
          }
          if (event.key === "ArrowLeft") {
            event.preventDefault();
            changePage(page - 1);
          }
        }}
      >
        {failed ? (
          <div className="document-preview-placeholder">
            <FailureState
              scope="region"
              error={failed}
              intent="read"
              resource="document"
              pending={reading}
              onRetry={onRetry}
            />
          </div>
        ) : (
          <>
            {!painted && (
              <div className="document-preview-loading">
                <LoadingState scope="inline" label="Loading preview…" />
              </div>
            )}
            {current && (
              <div className="document-viewer-page">
                <canvas
                  ref={canvas}
                  title={`Preview of ${name}`}
                  role="img"
                  aria-label={`${name}, page ${page} of ${model?.pages || 1}`}
                  style={{
                    width: current.width * clamped,
                    height: current.height * clamped,
                  }}
                />
                <p className="sr-only" aria-live="polite">
                  {current.text}
                </p>
              </div>
            )}
          </>
        )}
      </div>
      <div className="document-viewer-controls" aria-label="Document controls">
        <div className="document-viewer-pages">
          {model && model.pages > 1 && (
            <Button
              color="tertiary"
              size="sm"
              iconLeading={ChevronLeft}
              aria-label="Previous document page"
              title="Previous page"
              isDisabled={!ready || page === 1}
              onPress={() => changePage(page - 1)}
            />
          )}
          <span
            className="vult-type-metadata text-secondary"
            aria-live="polite"
          >
            {model ? `${page} / ${model.pages}` : "—"}
          </span>
          {model && model.pages > 1 && (
            <Button
              color="tertiary"
              size="sm"
              iconLeading={ChevronRight}
              aria-label="Next document page"
              title="Next page"
              isDisabled={!ready || page === model.pages}
              onPress={() => changePage(page + 1)}
            />
          )}
        </div>
        <div className="document-viewer-zoom">
          {!recall && (
            <>
              <Button
                color="tertiary"
                size="sm"
                iconLeading={ZoomOut}
                aria-label="Zoom out"
                title="Zoom out"
                isDisabled={!ready || clamped <= minimumZoom}
                onPress={() => setFit(Math.max(minimumZoom, clamped / 1.25))}
              />
              <Button
                color="tertiary"
                size="sm"
                aria-label="Fit document to page"
                title="Fit whole page"
                isDisabled={!ready}
                onPress={() => setFit("page")}
              >
                {typeof fit === "number"
                  ? `${Math.round(clamped * 100)}%`
                  : "Fit"}
              </Button>
              <Button
                color="tertiary"
                size="sm"
                iconLeading={ZoomIn}
                aria-label="Zoom in"
                title="Zoom in"
                isDisabled={!ready || clamped >= maximumZoom}
                onPress={() => setFit(Math.min(maximumZoom, clamped * 1.25))}
              />
            </>
          )}
          {!expanded && (
            <Button
              color="tertiary"
              size="sm"
              iconLeading={Expand01}
              aria-label="Expand document"
              title="Expand document"
              isDisabled={!ready}
              onPress={(event) =>
                onExpand(
                  event.target instanceof HTMLElement
                    ? event.target.closest("button")
                    : null,
                )
              }
            />
          )}
          {expanded && (
            <Button
              color="tertiary"
              size="sm"
              aria-label="Fit document to width"
              title="Fit width"
              isDisabled={!ready}
              onPress={() => setFit("width")}
            >
              Width
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
