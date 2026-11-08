import { useCallback, useEffect, useRef, useState } from "react";

/** Read retries cannot overlap or restart immediately after a fast failure. */
export function useReadRetry<Args extends unknown[]>(
  action: (...args: Args) => unknown,
  pending = false,
) {
  const current = useRef(action);
  current.current = action;
  const blocked = useRef(pending);
  blocked.current = pending;
  const locked = useRef(false);
  const alive = useRef(true);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [running, setRunning] = useState(false);
  const [coolingDown, setCoolingDown] = useState(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      clearTimeout(timer.current);
    };
  }, []);
  const retry = useCallback(async (...args: Args) => {
    if (blocked.current || locked.current) return;
    locked.current = true;
    setRunning(true);
    try {
      await current.current(...args);
    } finally {
      if (alive.current) {
        setRunning(false);
        setCoolingDown(true);
        timer.current = setTimeout(() => {
          locked.current = false;
          setCoolingDown(false);
        }, 1000);
      }
    }
  }, []);
  return { retry, running, coolingDown };
}
