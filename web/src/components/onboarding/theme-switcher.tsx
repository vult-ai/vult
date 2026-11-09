import { Button as AriaButton } from "react-aria-components";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { Monitor01, Moon01, Sun } from "../../design-system/icons";
import { useTheme } from "../../design-system/theme";
import { normalizeThemePreference } from "../../design-system/theme-preference";

const themes = [
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon01 },
  { id: "system", label: "System", icon: Monitor01 },
] as const;

/** The theme control in the first-run top bar, the same one the sign-in pages show. */
export function ThemeSwitcher() {
  const theme = useTheme();
  const selected = themes.find((option) => option.id === theme.preference) ?? themes[2];
  const Icon = selected.icon;
  return (
    <Dropdown.Root>
      <AriaButton
        aria-label={`Theme: ${selected.label}`}
        className="vult-action-target vult-action-fill outline-focus-ring flex size-9 shrink-0 cursor-pointer items-center justify-center rounded-lg text-secondary"
      >
        <Icon className="size-5" aria-hidden="true" />
      </AriaButton>
      <Dropdown.Popover placement="bottom right" offset={8}>
        <Dropdown.Menu
          aria-label="Theme"
          selectionMode="single"
          disallowEmptySelection
          selectedKeys={[selected.id]}
          onSelectionChange={(keys) => {
            if (keys === "all") return;
            const next = normalizeThemePreference(String(Array.from(keys)[0]));
            if (next) theme.setPreference(next);
          }}
        >
          {themes.map((option) => (
            <Dropdown.Item key={option.id} id={option.id} icon={option.icon}>
              {option.label}
            </Dropdown.Item>
          ))}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown.Root>
  );
}
