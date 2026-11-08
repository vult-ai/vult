/** React Aria may scroll a grid's sticky action cell in response to focus. */
export function focusWithoutScroll(target: HTMLElement) {
  if (!target.isConnected) return;
  const positions: { element: HTMLElement; left: number; top: number }[] = [];
  for (
    let element: HTMLElement | null = target.parentElement;
    element;
    element = element.parentElement
  ) {
    positions.push({
      element,
      left: element.scrollLeft,
      top: element.scrollTop,
    });
  }
  const window = target.ownerDocument.defaultView;
  const page = window && { x: window.scrollX, y: window.scrollY };
  target.focus({ preventScroll: true });
  for (const position of positions) {
    position.element.scrollLeft = position.left;
    position.element.scrollTop = position.top;
  }
  if (
    window &&
    page &&
    (window.scrollX !== page.x || window.scrollY !== page.y)
  )
    window.scrollTo(page.x, page.y);
}
