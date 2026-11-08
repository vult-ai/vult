import { VultLogoMark } from "../../design-system/vult-logo-mark";
import { VultWordmark } from "../../design-system/vult-wordmark";
import "./vult-brand.css";

/** The approved lockup. Its mark is 20px where the logo stands on its own and
 * 16px beside text, as in the navigation rail. */
export function VultBrand({ rail = false }: { rail?: boolean }) {
  return (
    <span className="vult-brand" data-size={rail ? "16" : "20"}>
      <VultLogoMark aria-hidden className="vult-brand-mark" />
      <span className={rail ? "vult-rail-label vult-brand-word" : "vult-brand-word"}>
        <VultWordmark role="img" aria-label="Vult" />
      </span>
    </span>
  );
}
