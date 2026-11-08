"use client";

import {
  useEffect,
  useRef,
  useState,
  useTransition,
  type MouseEvent,
} from "react";
import { useFormStatus } from "react-dom";

export function useButtonAction<E>(
  action: ((event: E) => unknown) | undefined,
  disabled?: boolean,
  type: "button" | "submit" | "reset" = "button",
) {
  const inFlight = useRef(false);
  const submitting = useRef(false);
  const [transitionPending, startTransition] = useTransition();
  const form = useFormStatus();
  const formPending = type === "submit" && form.pending;
  const pending = transitionPending || formPending;
  const [status, setStatus] = useState<"idle" | "running" | "settled">("idle");

  useEffect(() => {
    if (pending || status !== "settled") return;
    inFlight.current = false;
    setStatus("idle");
  }, [pending, status]);

  function press(event: E) {
    if (disabled || pending || inFlight.current || !action) return;
    // Lock before React renders so presses in the same event loop cannot overlap.
    inFlight.current = true;
    let result: unknown;
    try {
      // Local changes must paint immediately, even while another action is pending.
      result = action(event);
    } catch (error) {
      inFlight.current = false;
      throw error;
    }
    if (
      !result ||
      typeof (result as PromiseLike<unknown>).then !== "function"
    ) {
      // A local change has no pending operation. Keep its label, focus and
      // enabled state stable, while deduplicating this event's handlers.
      queueMicrotask(() => {
        inFlight.current = false;
      });
      return;
    }
    setStatus("running");
    startTransition(() => {
      // Track router work from this press; request promises keep their own lock.
      void Promise.resolve(result).then(
        () => setStatus("settled"),
        (error) => {
          setStatus("settled");
          startTransition(() => {
            throw error;
          });
        },
      );
    });
  }

  function captureSubmit(event: MouseEvent<HTMLButtonElement>) {
    if (disabled || formPending || submitting.current) {
      event.preventDefault();
      return;
    }
    // Preserve the first native submit; block repeated clicks before form state renders.
    submitting.current = true;
    queueMicrotask(() => {
      submitting.current = false;
    });
  }

  // Native submit/reset follows onPress; disabling here would cancel that default action.
  return {
    pending: type === "button" ? pending || status !== "idle" : formPending,
    onPress: action ? press : undefined,
    onClickCapture: type === "submit" ? captureSubmit : undefined,
  };
}
