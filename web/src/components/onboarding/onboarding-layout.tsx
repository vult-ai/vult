import type { ReactNode } from "react";
import { VultBrand } from "../ui/vult-brand";
import { ProfileMenu } from "../shell/profile-menu";
import { OnboardingParticles } from "./onboarding-particles";
import "./onboarding.css";

/** The book must be known before exposing dashboard navigation. */
export function OnboardingLayout({
  children,
  mainId = "main-content",
}: {
  children: ReactNode;
  mainId?: string;
}) {
  return (
    <div className="onboarding">
      <OnboardingParticles
        className="onboarding-particles"
        aria-hidden="true"
        color="#666666"
        ease={20}
        quantity={120}
      />
      <header className="onboarding-header">
        <ProfileMenu />
      </header>
      <main id={mainId} className="onboarding-main" tabIndex={-1}>
        <div className="onboarding-content">
          <div className="onboarding-brand">
            <VultBrand />
          </div>
          {children}
        </div>
      </main>
    </div>
  );
}
