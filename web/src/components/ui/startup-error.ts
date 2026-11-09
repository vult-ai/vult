import { presentFailureState } from "../../lib/errors";
import "../../design-system/styles/vult-controls.css";
import "./state.css";

/**
 * Native page failure state: still works when the application/React chunk
 * cannot load. Same anatomy and classes as FailureState at page scope.
 */
export function showStartupError(
  root: HTMLElement,
  error: unknown,
  copyOverride?: { title: string; detail: string },
) {
  const state = presentFailureState(error, { intent: "read", resource: "app" });
  const frame = document.createElement("main");
  frame.dataset.severity = state.severity;
  frame.className = "vult-studio startup-error-screen";
  const panel = document.createElement("section");
  panel.className = "vult-state failure-state";
  panel.dataset.kind = "failure";
  panel.dataset.scope = "page";
  panel.dataset.severity = state.severity;
  panel.dataset.tone =
    state.severity === "critical"
      ? "critical"
      : state.severity === "attention"
        ? "strong"
        : "quiet";
  panel.setAttribute("aria-labelledby", "startup-error-title");
  const icon = document.createElement("span");
  icon.className = "vult-state-icon";
  icon.setAttribute("aria-hidden", "true");
  // A native outlined mark remains available without the React/icon chunk.
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  for (const [name, value] of Object.entries({
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "2",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
  }))
    svg.setAttribute(name, value);
  const outline = document.createElementNS(
    ns,
    state.severity === "critical" ? "polygon" : "circle",
  );
  if (state.severity === "critical")
    outline.setAttribute("points", "8,2 16,2 22,8 22,16 16,22 8,22 2,16 2,8");
  else
    for (const [name, value] of Object.entries({ cx: "12", cy: "12", r: "10" }))
      outline.setAttribute(name, value);
  const mark = document.createElementNS(ns, "path");
  mark.setAttribute("d", "M12 8v4m0 4h.01");
  svg.append(outline, mark);
  icon.append(svg);
  const copy = document.createElement("div");
  copy.className = "vult-state-copy";
  copy.setAttribute("role", "alert");
  copy.setAttribute("aria-atomic", "true");
  const title = document.createElement("h1");
  title.id = "startup-error-title";
  title.className = "vult-state-title vult-type-section-title";
  title.textContent = copyOverride?.title ?? state.title;
  title.tabIndex = -1;
  const detail = document.createElement("p");
  detail.className = "vult-state-detail vult-type-ui";
  detail.textContent = copyOverride?.detail ?? state.detail ?? "";
  copy.append(title, detail);
  const actions = document.createElement("div");
  actions.className = "vult-state-actions";
  const reload = document.createElement("button");
  reload.type = "button";
  reload.className = "vult-button vult-button-primary";
  const label = document.createElement("span");
  label.className = "vult-button-copy";
  label.textContent = "Reload Vult";
  reload.append(label);
  reload.addEventListener("click", () => window.location.reload());
  actions.append(reload);
  panel.append(icon, copy, actions);
  frame.append(panel);
  root.replaceChildren(frame);
  title.focus({ preventScroll: true });
}
