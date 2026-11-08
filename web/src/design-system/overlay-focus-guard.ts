// Browsers can deliver window focus just before the pointer event that caused it.
// Compare event creation times: a busy page may process that pointer event much later.
const WINDOW_ACTIVATION_MS = 100;
const OVERLAY_SELECTOR =
  '[role="dialog"], [data-rac][data-placement]:not([role="tooltip"])';

export function installOverlayFocusGuard(document: Document) {
  const window = document.defaultView;
  if (!window) return () => {};

  let inactive = !document.hasFocus();
  let focusedAt = -Infinity;
  let protectGesture = false;
  const capture = { capture: true };

  const onBlur = () => {
    inactive = true;
    focusedAt = -Infinity;
    protectGesture = false;
  };
  const onFocus = (event: FocusEvent) => {
    if (inactive) focusedAt = event.timeStamp;
    inactive = false;
  };
  const suppress = (event: Event) => {
    event.preventDefault();
    event.stopImmediatePropagation();
  };
  const onPointerDown = (event: PointerEvent) => {
    const activating =
      inactive ||
      !document.hasFocus() ||
      Math.abs(event.timeStamp - focusedAt) < WINDOW_ACTIVATION_MS;
    inactive = false;
    focusedAt = -Infinity;
    protectGesture = false;

    if (!activating || event.button !== 0 || event.pointerType === "touch")
      return;

    const overlays = document.querySelectorAll<HTMLElement>(OVERLAY_SELECTOR);
    const overlay = overlays[overlays.length - 1];
    if (overlay && !event.composedPath().includes(overlay)) {
      protectGesture = true;
      suppress(event);
    }
  };
  const onGestureEvent = (event: Event) => {
    if (protectGesture) suppress(event);
  };
  const onClick = (event: MouseEvent) => {
    if (protectGesture && event.detail > 0) suppress(event);
    protectGesture = false;
  };
  const reset = () => {
    focusedAt = -Infinity;
    protectGesture = false;
  };

  // Window capture runs before React Aria's document-level outside-click handlers.
  window.addEventListener("blur", onBlur);
  window.addEventListener("focus", onFocus);
  window.addEventListener("pointerdown", onPointerDown, capture);
  window.addEventListener("mousedown", onGestureEvent, capture);
  window.addEventListener("pointerup", onGestureEvent, capture);
  window.addEventListener("mouseup", onGestureEvent, capture);
  window.addEventListener("click", onClick, capture);
  window.addEventListener("pointercancel", reset, capture);
  window.addEventListener("keydown", reset, capture);

  return () => {
    window.removeEventListener("blur", onBlur);
    window.removeEventListener("focus", onFocus);
    window.removeEventListener("pointerdown", onPointerDown, capture);
    window.removeEventListener("mousedown", onGestureEvent, capture);
    window.removeEventListener("pointerup", onGestureEvent, capture);
    window.removeEventListener("mouseup", onGestureEvent, capture);
    window.removeEventListener("click", onClick, capture);
    window.removeEventListener("pointercancel", reset, capture);
    window.removeEventListener("keydown", reset, capture);
  };
}
