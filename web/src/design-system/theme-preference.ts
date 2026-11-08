export const THEME_COOKIE_KEY = "vult-theme";
export const THEME_RESOLVED_COOKIE_KEY = "vult-theme-resolved";
export const THEME_STORAGE_KEY = "vult-theme";
export const LIGHT_THEME_CLASS = "light-mode";
export const DARK_THEME_CLASS = "dark-mode";

export type ThemeName = "light" | "dark";
export type ThemePreference = "system" | ThemeName;

export function normalizeThemeName(
  value: string | null | undefined,
): ThemeName | null {
  if (value === "light" || value === LIGHT_THEME_CLASS) return "light";
  if (value === "dark" || value === DARK_THEME_CLASS) return "dark";

  return null;
}

export function normalizeThemePreference(
  value: string | null | undefined,
): ThemePreference | null {
  if (value === "system") return "system";

  return normalizeThemeName(value);
}

export function resolveThemePreference(
  preference: ThemePreference,
  systemTheme: ThemeName,
): ThemeName {
  return preference === "system" ? systemTheme : preference;
}

export function themeClassName(theme: ThemeName) {
  return theme === "dark" ? DARK_THEME_CLASS : LIGHT_THEME_CLASS;
}
