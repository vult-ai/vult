import { useEffect, useState } from "react";
import { Toaster as Sonner, toast as sonner } from "sonner";
import { AlertTriangle, CheckCircle, InfoCircle } from "../../design-system/icons";
import "./toaster.css";

/** The app's one toast stack: bottom right, following light and dark mode. */
export function Toaster() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark-mode"));
  useEffect(() => {
    const root = document.documentElement;
    const watch = new MutationObserver(() => setDark(root.classList.contains("dark-mode")));
    watch.observe(root, { attributes: true, attributeFilter: ["class"] });
    return () => watch.disconnect();
  }, []);
  return (
    <Sonner
      position="bottom-right"
      theme={dark ? "dark" : "light"}
      duration={4000}
      gap={8}
      icons={{
        success: <CheckCircle aria-hidden />,
        info: <InfoCircle aria-hidden />,
        warning: <AlertTriangle aria-hidden />,
      }}
      toastOptions={{ className: "vult-toast" }}
    />
  );
}

/** A short confirmation that something finished, in one sentence. */
export const toast = {
  success: (message: string) => sonner.success(message),
  info: (message: string) => sonner.info(message),
  caution: (message: string) => sonner.warning(message),
};
