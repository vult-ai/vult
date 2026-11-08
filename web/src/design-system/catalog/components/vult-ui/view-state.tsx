"use client";

import {
  createContext,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
  useCallback,
  useContext,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

export type ViewStateStorage = "session" | "local";
export interface ViewStateAdapter {
  read(scope: string, name: string, storage: ViewStateStorage): unknown;
  write(
    scope: string,
    name: string,
    value: unknown,
    storage: ViewStateStorage,
  ): void;
}
type Context = {
  scope: string;
  adapter?: ViewStateAdapter;
  initialValues: Record<string, unknown>;
};
type Codec<T> = {
  encode: (value: T) => unknown;
  decode: (value: unknown) => T | undefined;
};
export type ViewStateOptions<T> = {
  enabled?: boolean;
  valid?: (value: unknown) => value is T;
  codec?: Codec<T>;
  storage?: "local";
};
const ViewStateContext = createContext<Context | null>(null);
const emptyValues: Record<string, unknown> = {};

/** Hosts own identity, storage and SSR seeds. Without an adapter, state stays in memory. */
export function ViewStateProvider({
  scope,
  adapter,
  initialValues = emptyValues,
  children,
}: {
  scope: string;
  adapter?: ViewStateAdapter;
  initialValues?: Record<string, unknown>;
  children: ReactNode;
}) {
  const context = useMemo(
    () => (scope.trim() ? { scope, adapter, initialValues } : null),
    [scope, adapter, initialValues],
  );
  return (
    <ViewStateContext.Provider value={context}>
      {children}
    </ViewStateContext.Provider>
  );
}

function decode<T>(raw: unknown, initial: T, options: ViewStateOptions<T>): T {
  try {
    if (options.codec) return options.codec.decode(raw) ?? initial;
    if (options.valid?.(raw)) return raw;
  } catch {
    /* A broken preference cannot prevent opening a view. */
  }
  return initial;
}

/** Restores presentation only. A stale setter can never write into a different owner scope. */
export function useViewState<T>(
  name: string,
  initial: T | (() => T),
  options: ViewStateOptions<T> = {},
): [T, Dispatch<SetStateAction<T>>, boolean] {
  const provider = useContext(ViewStateContext);
  const context = options.enabled === false ? null : provider;
  const identity = JSON.stringify([context?.scope ?? "memory", name]);
  const initialValue = useRef<{ identity: string; value: T } | null>(null);
  if (!initialValue.current || initialValue.current.identity !== identity) {
    const fallback =
      typeof initial === "function" ? (initial as () => T)() : initial;
    initialValue.current = {
      identity,
      value: decode(context?.initialValues[name], fallback, options),
    };
  }
  const [snapshot, setSnapshot] = useState({
    identity: context?.adapter ? (null as string | null) : identity,
    value: initialValue.current.value,
  });
  const value =
    snapshot.identity === identity
      ? snapshot.value
      : initialValue.current.value;
  const current = useRef(value);
  current.current = value;
  const latest = useRef({ identity, context, name, options });
  latest.current = { identity, context, name, options };
  const live = useRef(false);
  useLayoutEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const persist = useCallback((next: T) => {
    const target = latest.current;
    if (!target.context?.adapter) return;
    let encoded: unknown;
    try {
      encoded = target.options.codec ? target.options.codec.encode(next) : next;
    } catch {
      return;
    }
    const { scope, adapter } = target.context;
    try {
      adapter.write(scope, target.name, encoded, "session");
    } catch {
      /* Storage is optional. */
    }
    if (target.options.storage === "local") {
      try {
        adapter.write(scope, target.name, encoded, "local");
      } catch {
        /* Editing still works with blocked storage. */
      }
    }
  }, []);

  useLayoutEffect(() => {
    if (!context?.adapter) {
      if (snapshot.identity !== identity)
        setSnapshot({ identity, value: initialValue.current!.value });
      return;
    }
    let raw: unknown;
    if (context?.adapter) {
      if (options.storage === "local") {
        try {
          raw = context.adapter.read(context.scope, name, "local");
        } catch {}
      }
      if (raw === undefined) {
        try {
          raw = context.adapter.read(context.scope, name, "session");
        } catch {}
      }
    }
    const restored = decode(raw, initialValue.current!.value, options);
    current.current = restored;
    setSnapshot({ identity, value: restored });
    persist(restored);
  }, [identity, context?.adapter, options.storage, persist]);
  const ready = snapshot.identity === identity;
  const change = useCallback<Dispatch<SetStateAction<T>>>(
    (next) => {
      if (!live.current || latest.current.identity !== identity) return;
      const resolved =
        typeof next === "function"
          ? (next as (previous: T) => T)(current.current)
          : next;
      current.current = resolved;
      persist(resolved);
      setSnapshot({ identity, value: resolved });
    },
    [identity, persist],
  );
  return [value, change, ready];
}
