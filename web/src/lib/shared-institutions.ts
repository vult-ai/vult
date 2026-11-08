export type Institution = {
  id: string;
  name: string;
  domain: string;
  aliases?: string[];
  asset: {
    sha256: string;
    media_type: "image/svg+xml" | "image/png" | "image/webp";
    bytes: number;
  };
  presentation: {
    mode: "image" | "monochrome";
    light: { background: string; foreground: string };
    dark: { background: string; foreground: string };
  };
  source_url: string;
  source_page?: string;
};
export type InstitutionCatalog = { version: 1; institutions: Institution[] };

/** The development mirror and the shared asset origin use identical object keys. */
export function institutionAssetKey(institution: Institution) {
  const extension = {
    "image/svg+xml": "svg",
    "image/png": "png",
    "image/webp": "webp",
  }[institution.asset.media_type];
  return `sha256/${institution.asset.sha256}.${extension}`;
}

export function sharedAssetOrigin(raw: string, development = false) {
  if (!raw) return "";
  if (development && raw === "/assets/shared") return raw;
  try {
    const url = new URL(raw);
    if (
      url.protocol === "https:" &&
      /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(
        url.hostname,
      ) &&
      url.origin === raw &&
      !url.username &&
      !url.password &&
      url.hostname !== "localhost" &&
      !["127.0.0.1", "[::1]"].includes(url.hostname)
    )
      return raw;
  } catch {
    /* Invalid configuration never becomes a local production fallback. */
  }
  return "";
}
export function configuredSharedAssetOrigin() {
  return sharedAssetOrigin(
    document.querySelector<HTMLMetaElement>(
      'meta[name="vult-shared-asset-origin"]',
    )?.content || "",
    document.querySelector<HTMLMetaElement>(
      'meta[name="vult-development-assets"]',
    )?.content === "1",
  );
}
export function institutionAssetURL(base: string, institution: Institution) {
  return `${base}/${institutionAssetKey(institution)}`;
}

export function parseInstitutionCatalog(text: string): InstitutionCatalog {
  if (text.length > 131_072)
    throw new Error("Institution catalog is too large.");
  const value = JSON.parse(text);
  if (
    value?.version !== 1 ||
    !Array.isArray(value.institutions) ||
    value.institutions.length > 200
  )
    throw new Error("Institution catalog is invalid.");
  const ids = new Set<string>();
  const institutions: Institution[] = value.institutions.map(
    (item: Institution) => {
      if (
        !item ||
        !/^[a-z][a-z0-9-]{0,63}$/.test(item.id) ||
        ids.has(item.id) ||
        typeof item.name !== "string" ||
        !item.name.trim() ||
        item.name.length > 100 ||
        typeof item.domain !== "string" || (item.domain !== "" && !/^[a-z0-9.-]+\.[a-z]{2,}$/.test(item.domain)) ||
        (item.aliases !== undefined &&
          (!Array.isArray(item.aliases) ||
            item.aliases.length > 50 ||
            item.aliases.some(
              (alias) =>
                typeof alias !== "string" ||
                !alias.trim() ||
                [...alias].length > 100,
            ))) ||
        !/^[a-f0-9]{64}$/.test(item.asset?.sha256) ||
        !["image/svg+xml", "image/png", "image/webp"].includes(
          item.asset.media_type,
        ) ||
        !Number.isSafeInteger(item.asset.bytes) ||
        item.asset.bytes < 1 ||
        item.asset.bytes > 262_144 ||
        typeof item.source_url !== "string" ||
        item.source_url.length > 2048
      )
        throw new Error("Institution catalog is invalid.");
      const presentation = item.presentation;
      const color = (value: unknown) =>
        typeof value === "string" && /^#[a-f0-9]{6}$/i.test(value);
      if (
        !presentation ||
        !["image", "monochrome"].includes(presentation.mode) ||
        (item.asset.media_type !== "image/svg+xml" &&
          presentation.mode !== "image") ||
        Object.keys(presentation).some(
          (key) => !["mode", "light", "dark"].includes(key),
        ) ||
        !color(presentation.light?.background) ||
        !color(presentation.light?.foreground) ||
        !color(presentation.dark?.background) ||
        !color(presentation.dark?.foreground) ||
        [
          ...Object.keys(presentation.light),
          ...Object.keys(presentation.dark),
        ].some((key) => !["background", "foreground"].includes(key))
      )
        throw new Error("Institution presentation is invalid.");
      const source = new URL(item.source_url);
      const page = new URL(item.source_page || item.source_url);
      if (
        source.protocol !== "https:" ||
        source.username ||
        source.password ||
        source.port || !/^[a-z0-9.-]+\.[a-z]{2,}$/.test(source.hostname) ||
        page.protocol !== "https:" || page.username || page.password || page.port ||
        !/^[a-z0-9.-]+\.[a-z]{2,}$/.test(page.hostname) ||
        (item.domain && page.hostname !== item.domain && !page.hostname.endsWith(`.${item.domain}`))
      )
        throw new Error("Institution provenance is invalid.");
      ids.add(item.id);
      return {
        id: item.id,
        name: item.name,
        domain: item.domain,
        ...(item.aliases ? { aliases: [...item.aliases] } : {}),
        asset: {
          sha256: item.asset.sha256,
          media_type: item.asset.media_type,
          bytes: item.asset.bytes,
        },
        presentation: {
          mode: presentation.mode,
          light: {
            background: presentation.light.background,
            foreground: presentation.light.foreground,
          },
          dark: {
            background: presentation.dark.background,
            foreground: presentation.dark.foreground,
          },
        },
        source_url: item.source_url,
        ...(item.source_page ? { source_page: item.source_page } : {}),
      };
    },
  );
  return { version: 1, institutions };
}
export const INSTITUTION_CATALOG_TTL = 10 * 60 * 1000;
const catalogs = new Map<
  string,
  { pending?: Promise<InstitutionCatalog>; value?: InstitutionCatalog; expires: number }
>();
const catalogStorageKey = (file: string) => `vult:public-avatar-catalog:v1:${file}`;

/** This cache contains validated public catalog bytes only, never Book data. */
export function cachedInstitutionCatalog(
  base: string,
  file: "catalog.v1.json" | "brand-logos.v1.json" = "catalog.v1.json",
) {
  if (!base) return undefined;
  const key = `${base}/${file}`;
  let cached = catalogs.get(key);
  if (!cached) {
    try {
      const raw = window.sessionStorage.getItem(catalogStorageKey(file));
      if (raw && raw.length < 262_144) {
        const stored = JSON.parse(raw);
        if (stored.base === base && Number.isFinite(stored.expires) &&
          stored.expires <= Date.now() + INSTITUTION_CATALOG_TTL &&
          stored.expires > Date.now() - 24 * 60 * 60 * 1000) {
          cached = { value: parseInstitutionCatalog(JSON.stringify(stored.catalog)), expires: stored.expires };
          catalogs.set(key, cached);
        }
      }
    } catch { /* Disabled storage or invalid public data uses the normal read. */ }
  }
  return cached && cached.expires > Date.now() - 24 * 60 * 60 * 1000
    ? cached.value : undefined;
}

export function readInstitutionCatalog(
  base: string,
  retry = false,
  file: "catalog.v1.json" | "brand-logos.v1.json" = "catalog.v1.json",
) {
  const cacheKey = `${base}/${file}`;
  cachedInstitutionCatalog(base, file);
  const cached = catalogs.get(cacheKey);
  if (!retry && cached?.value && cached.expires > Date.now()) return Promise.resolve(cached.value);
  let pending =
    !retry && cached && cached.expires > Date.now() ? cached.pending : undefined;
  if (!pending) {
    pending = (async () => {
      const options = {
        credentials: "omit",
        referrerPolicy: "no-referrer",
      } as const;
      let response = await fetch(`${base}/${file}`, options);
      if (file === "brand-logos.v1.json" && response.status === 404)
        response = await fetch(`${base}/catalog.v1.json`, options);
      if (
        !response.ok ||
        !response.headers.get("content-type")?.includes("application/json") ||
        Number(response.headers.get("content-length")) > 131_072
      )
        throw new Error("Institution logos are unavailable. Try again.");
      return parseInstitutionCatalog(await response.text());
    })();
    const entry: { pending?: Promise<InstitutionCatalog>; value?: InstitutionCatalog; expires: number } = {
      pending,
      value: cached?.value,
      expires: Date.now() + INSTITUTION_CATALOG_TTL,
    };
    catalogs.set(cacheKey, entry);
    pending.then((catalog) => {
      if (catalogs.get(cacheKey) !== entry) return;
      entry.value = catalog;
      entry.pending = undefined;
      entry.expires = Date.now() + INSTITUTION_CATALOG_TTL;
      try {
        window.sessionStorage.setItem(catalogStorageKey(file), JSON.stringify({base, expires:entry.expires, catalog}));
      } catch { /* Public caching is optional. */ }
    }, () => {
      if (catalogs.get(cacheKey) !== entry) return;
      if (!entry.value) catalogs.delete(cacheKey);
      else { entry.pending = undefined; entry.expires = cached?.expires || 0; }
    });
  }
  return pending;
}
