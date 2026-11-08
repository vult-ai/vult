import { useEffect, useState } from "react";
import {
  configuredSharedAssetOrigin,
  cachedInstitutionCatalog,
  INSTITUTION_CATALOG_TTL,
  readInstitutionCatalog,
  type Institution,
} from "./shared-institutions";

export function useInstitutionCatalog(
  active = true,
  purpose: "accounts" | "brands" = "accounts",
) {
  const base = configuredSharedAssetOrigin();
  const file = purpose === "brands" ? "brand-logos.v1.json" : "catalog.v1.json";
  const key = `${base}/${file}`;
  const [retry, setRetry] = useState(0);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    if (!active || !base) return;
    const reload = () => {
      if (!document.hidden) setRefresh((previous) => previous + 1);
    };
    const timer = window.setInterval(reload, INSTITUTION_CATALOG_TTL);
    document.addEventListener("visibilitychange", reload);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", reload);
    };
  }, [active, base]);
  const [state, setState] = useState<{
    base: string;
    key: string;
    institutions: Institution[];
    status: "loading" | "ready" | "error" | "unconfigured";
  }>(() => {
    const cached = cachedInstitutionCatalog(base, file);
    return { base, key, institutions: cached?.institutions || [],
      status: cached ? "ready" : base ? "loading" : "unconfigured" };
  });
  useEffect(() => {
    if (!base || !active) return;
    let alive = true;
    setState((previous) => ({
      base,
      key,
      institutions: previous.key === key ? previous.institutions : cachedInstitutionCatalog(base, file)?.institutions || [],
      status: previous.key === key && previous.institutions.length ? previous.status : "loading",
    }));
    readInstitutionCatalog(base, retry > 0, file)
      .then((catalog) => {
        if (alive)
          setState({
            base,
            key,
            institutions: catalog.institutions,
            status: "ready",
          });
      })
      .catch(() => {
        if (alive)
          setState((previous) => ({ ...previous, base, status: "error" }));
      });
    return () => {
      alive = false;
    };
  }, [base, active, retry, refresh, file, key]);
  const cached = cachedInstitutionCatalog(base, file);
  const current = state.key === key
      ? state
      : {
          base,
          key,
          institutions: cached?.institutions || [],
          status: base ? ("loading" as const) : ("unconfigured" as const),
        };
  return {
    ...(current.institutions.length || !cached ? current : {
      ...current, institutions: cached.institutions,
      status: current.status === "error" ? "error" as const : "ready" as const,
    }),
    retry: () => setRetry((value) => value + 1),
  };
}
