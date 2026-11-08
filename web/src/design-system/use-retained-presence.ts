import { useLayoutEffect, useRef, useState, type RefObject } from "react";

/** One presence owner for portaled surfaces. CSS, rather than a timer, owns exit. */
export function useRetainedPresence(
  open: boolean,
  element: RefObject<HTMLElement | null>,
) {
  const [retained, setRetained] = useState(open);
  const currentOpen = useRef(open);
  currentOpen.current = open;
  if (open && !retained) setRetained(true);
  useLayoutEffect(() => {
    if (open || !retained) return;
    let current = true;
    const animations = (element.current?.getAnimations?.() ?? []).filter(
      (animation) => animation.playState === "running",
    );
    if (!animations.length) {
      setRetained(false);
      return;
    }
    void Promise.all(
      animations.map((animation) => animation.finished.catch(() => {})),
    ).then(() => {
      if (current && !currentOpen.current) setRetained(false);
    });
    return () => {
      current = false;
    };
  }, [open, retained, element]);
  return open || retained;
}
