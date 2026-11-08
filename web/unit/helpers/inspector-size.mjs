/** Give only the real inspector a measurable workspace; leave source table sizing alone. */
export function inspectorSize(window, initialWidth) {
  let width = initialWidth;
  const listeners = new Set();
  const originalRect = window.HTMLElement.prototype.getBoundingClientRect;
  const originalObserver = globalThis.ResizeObserver;
  const originalWindowObserver = window.ResizeObserver;
  window.HTMLElement.prototype.getBoundingClientRect = function () {
    return this.classList.contains("collection-inspector")
      ? new window.DOMRect(0, 0, width, 600)
      : originalRect.call(this);
  };
  const Observer = class {
    constructor(callback) {
      this.callback = callback;
    }
    observe(element) {
      if (element.classList.contains("collection-inspector"))
        listeners.add(this.callback);
    }
    unobserve() {
      listeners.delete(this.callback);
    }
    disconnect() {
      listeners.delete(this.callback);
    }
  };
  globalThis.ResizeObserver = window.ResizeObserver = Observer;
  return {
    set(value) {
      width = value;
      for (const listener of listeners) listener([]);
    },
    restore() {
      window.HTMLElement.prototype.getBoundingClientRect = originalRect;
      globalThis.ResizeObserver = originalObserver;
      window.ResizeObserver = originalWindowObserver;
    },
  };
}
