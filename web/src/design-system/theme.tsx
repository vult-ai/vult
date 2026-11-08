"use client";

import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  DARK_THEME_CLASS,
  LIGHT_THEME_CLASS,
  THEME_COOKIE_KEY,
  THEME_RESOLVED_COOKIE_KEY,
  THEME_STORAGE_KEY,
  type ThemeName,
  type ThemePreference,
  normalizeThemePreference,
  resolveThemePreference,
  themeClassName,
} from "./theme-preference";

export type { ThemeName, ThemePreference } from "./theme-preference";

interface ThemeContextValue {
  preference: ThemePreference;
  theme: ThemeName;
  setPreference: (preference: ThemePreference) => void;
  setTheme: (theme: ThemeName) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

function readSystemTheme(): ThemeName {
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

function readStoredPreference(fallback: ThemePreference): ThemePreference {
  try {
    return (
      normalizeThemePreference(
        window.localStorage.getItem(THEME_STORAGE_KEY),
      ) ?? fallback
    );
  } catch {
    return fallback;
  }
}

function applyTheme(preference: ThemePreference, theme: ThemeName) {
  const themeClass = themeClassName(theme);
  document.documentElement.classList.remove(
    LIGHT_THEME_CLASS,
    DARK_THEME_CLASS,
  );
  document.documentElement.classList.add(themeClass);
  document.documentElement.dataset.vultThemePreference = preference;
  document.documentElement.style.colorScheme = theme;
}

function persistThemeCookies(preference: ThemePreference, theme: ThemeName) {
  const secure = window.location.protocol === "https:" ? "; Secure" : "";
  const attributes = `; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
  document.cookie = `${THEME_COOKIE_KEY}=${preference}${attributes}`;
  document.cookie = `${THEME_RESOLVED_COOKIE_KEY}=${theme}${attributes}`;
}

interface ThemeProps {
  children: ReactNode;
  initialPreference: ThemePreference;
  initialTheme: ThemeName;
  preferenceOverride?: ThemePreference;
  onPreferenceChange?: (value: ThemePreference) => void;
  isolateStorage?: boolean;
}

export function Theme({
  children,
  initialPreference,
  initialTheme,
  preferenceOverride,
  onPreferenceChange,
  isolateStorage = false,
}: ThemeProps) {
  const [preference, setPreferenceState] =
    useState<ThemePreference>(initialPreference);
  const [theme, setResolvedTheme] = useState<ThemeName>(initialTheme);
  const preferenceRef = useRef(initialPreference);

  const applyPreference = useCallback(
    (nextPreference: ThemePreference, persistLocally: boolean) => {
      const nextTheme = resolveThemePreference(
        nextPreference,
        readSystemTheme(),
      );

      if (persistLocally) {
        try {
          window.localStorage.setItem(THEME_STORAGE_KEY, nextPreference);
        } catch {
          // The selection still applies for this session when storage is unavailable.
        }
      }

      preferenceRef.current = nextPreference;
      persistThemeCookies(nextPreference, nextTheme);
      applyTheme(nextPreference, nextTheme);
      setPreferenceState(nextPreference);
      setResolvedTheme(nextTheme);
    },
    [],
  );

  const setPreference = useCallback(
    (nextPreference: ThemePreference) => {
      if (onPreferenceChange) {
        onPreferenceChange(nextPreference);
        return;
      }
      if (isolateStorage) return;
      applyPreference(nextPreference, true);
    },
    [applyPreference, onPreferenceChange, isolateStorage],
  );

  const setTheme = useCallback(
    (nextTheme: ThemeName) => {
      setPreference(nextTheme);
    },
    [setPreference],
  );

  useLayoutEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const syncStoredPreference = () => {
      if (preferenceOverride !== undefined)
        applyPreference(preferenceOverride, false);
      else if (!isolateStorage)
        applyPreference(readStoredPreference(preferenceRef.current), true);
    };
    const syncSystemTheme = () => {
      if (preferenceRef.current === "system") applyPreference("system", false);
    };
    const syncStorage = (event: StorageEvent) => {
      if (isolateStorage || preferenceOverride !== undefined) return;
      if (event.key !== THEME_STORAGE_KEY && event.key !== null) return;

      applyPreference(
        normalizeThemePreference(event.newValue) ?? "system",
        false,
      );
    };

    syncStoredPreference();
    media.addEventListener("change", syncSystemTheme);
    window.addEventListener("storage", syncStorage);

    return () => {
      media.removeEventListener("change", syncSystemTheme);
      window.removeEventListener("storage", syncStorage);
    };
  }, [applyPreference, preferenceOverride, isolateStorage]);

  const value = useMemo(
    () => ({ preference, setPreference, setTheme, theme }),
    [preference, setPreference, setTheme, theme],
  );

  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  );
}

export function useTheme(): ThemeContextValue {
  const context = useContext(ThemeContext);
  if (!context) throw new Error("useTheme must be used within Theme");

  return context;
}
