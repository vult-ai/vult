import { useState, type CSSProperties } from "react";
import { cachedMarkFit, measureLoadedMark } from "../../lib/circular-mark-fit";
import {
  institutionAssetURL,
  type Institution,
} from "../../lib/shared-institutions";

/** One original URL at every size and for every account; no image derivatives. */
export function InstitutionLogo({
  base,
  institution,
  markSize,
}: {
  base: string;
  institution: Institution;
  markSize: number;
}) {
  const source = institutionAssetURL(base, institution);
  const [failed, setFailed] = useState("");
  const [loaded, setLoaded] = useState(() => ({
    source: cachedMarkFit(source) ? source : "",
    scale: 1,
    radiusRatio: 0,
    offsetX: 0,
    offsetY: 0,
    ...cachedMarkFit(source),
  }));
  if (!base || failed === source) return null;
  const { presentation } = institution;
  const ready = loaded.source === source;
  return (
    <span
      className="institution-logo"
      data-render={presentation.mode}
      data-loaded={ready || undefined}
      data-circle-fit={ready ? loaded.scale : undefined}
      data-ink-radius-ratio={ready ? loaded.radiusRatio : undefined}
      aria-hidden="true"
      style={
        {
          "--institution-background-light": presentation.light.background,
          "--institution-background-dark": presentation.dark.background,
          "--institution-foreground-light": presentation.light.foreground,
          "--institution-foreground-dark": presentation.dark.foreground,
          "--institution-source": `url("${source}")`,
          "--institution-circle-fit": ready ? loaded.scale : 1,
          "--institution-center-x": `${ready ? loaded.offsetX : 0}%`,
          "--institution-center-y": `${ready ? loaded.offsetY : 0}%`,
        } as CSSProperties
      }
    >
      <span className="institution-logo-viewport">
        <img
          src={source}
          alt=""
          title={institution.name}
          width={markSize}
          height={markSize}
          loading="lazy"
          decoding="async"
          crossOrigin="anonymous"
          referrerPolicy="no-referrer"
          onError={() => setFailed(source)}
          onLoad={(event) => {
            try {
              setLoaded({
                source,
                ...measureLoadedMark(event.currentTarget, source),
              });
            } catch {
              // Keep initials rather than reveal artwork with unverified clearance.
              setFailed(source);
            }
          }}
        />
        {presentation.mode === "monochrome" && (
          <span className="institution-logo-mark" />
        )}
      </span>
    </span>
  );
}
