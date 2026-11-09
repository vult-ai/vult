import { useEffect, useRef, useState, type ReactNode } from "react";
import { Menu01 as Menu, Settings01 as Settings } from "../../design-system/icons";
import { ShellHeaderFrame } from "../../design-system/shell-header-frame";
import { SidebarToggleControl } from "../../design-system/sidebar-toggle";
import { VultBrand } from "../ui/vult-brand";
import { SidebarContext } from "./sidebar-context";
import { Dialog } from "../ui/dialog";
import { Navigation, type NavigationItem } from "./navigation";
import { ProfileMenu } from "./profile-menu";
import { FollowDisplayPreferences } from "../../lib/user-preferences-context";
import type { PreferenceScope } from "../../lib/user-preferences";
import { HeaderActionsProvider } from "./header-actions";
import { useRailTooltip } from "./use-rail-tooltip";

const preferenceKey = "vult:sidebar-expanded:v1";

/** Presentation only: book state, query filters and operations stay with the host. */
export function AppShell({
  bookName,
  founderName,
  bookSwitcher,
  search,
  collection = false,
  ready,
  failed = false,
  active,
  navigation,
  currentPage,
  homeHref = "/dashboard",
  onNavigate,
  onSettings,
  onSettingsPage,
  userScope,
  sidebarCard,
  children,
}: {
  bookName?: string;
  founderName?: string;
  bookSwitcher?: ReactNode;
  search?: ReactNode;
  collection?: boolean;
  ready: boolean;
  /** The book could not be opened; the header must not claim it is opening. */
  failed?: boolean;
  active: string;
  navigation: readonly NavigationItem[];
  currentPage?: Pick<NavigationItem, "label" | "icon">;
  homeHref?: string;
  onNavigate?: (id: string) => void;
  onSettings: (trigger: HTMLElement) => void;
  onSettingsPage?: () => void;
  userScope?: PreferenceScope;
  /** Shown above Settings in the open sidebar and the phone drawer. */
  sidebarCard?: ReactNode;
  children: ReactNode;
}) {
  const [expanded, setExpanded] = useState(() => {
    try {
      return localStorage.getItem(preferenceKey) !== "false";
    } catch {
      return true;
    }
  });
  const [headerActions, setHeaderActions] = useState<HTMLDivElement | null>(
    null,
  );
  const railTooltip = useRailTooltip(expanded);
  const [animate, setAnimate] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const mobileTrigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const media = matchMedia("(min-width: 768px)");
    const close = () => {
      if (media.matches) setMobileOpen(false);
    };
    media.addEventListener("change", close);
    return () => media.removeEventListener("change", close);
  }, []);
  function toggle(shouldAnimate: boolean) {
    setAnimate(shouldAnimate);
    setExpanded((value) => {
      const next = !value;
      try {
        localStorage.setItem(preferenceKey, String(next));
      } catch {
        /* Layout remains usable with blocked storage. */
      }
      return next;
    });
  }
  const currentItem =
    currentPage ?? navigation.find((item) => item.id === active);
  const CurrentIcon = ready
    ? active === "settings"
      ? Settings
      : currentItem?.icon
    : undefined;
  const current = ready
    ? active === "settings"
      ? "Settings"
      : (currentItem?.label ?? "Activity")
    : bookName
      ? failed
        ? "Unavailable"
        : "Opening Book…"
      : "Create your Book";
  const nav = (mobile = false) => (
    <Navigation
      items={navigation}
      active={active}
      onNavigate={(id) => {
        onNavigate?.(id);
        if (mobile) setMobileOpen(false);
      }}
    />
  );
  const settings = (mobile = false) => (
    <button
      type="button"
      className="shell-navigation-link vult-action-target vult-focus-target vult-focus-contained"
      aria-label="Settings"
      disabled={!ready}
      aria-current={active === "settings" ? "page" : undefined}
      onClick={(event) => {
        if (mobile) setMobileOpen(false);
        if (onSettingsPage) onSettingsPage();
        else onSettings(mobile ? mobileTrigger.current! : event.currentTarget);
      }}
    >
      <span>
        <Settings className="size-4" aria-hidden />
        <span className="vult-rail-label">Settings</span>
      </span>
    </button>
  );
  return (
    <div className="vult-app-shell" data-vult-shell-body>
      <a href="#main-content" className="shell-skip-link">
        Skip to content
      </a>
      <aside
        {...railTooltip.handlers}
        id="app-sidebar"
        className="vult-shell-rail"
        data-expanded={expanded}
        data-animate={animate}
        aria-label="Sidebar"
      >
        <div className="vult-shell-panel">
          <div className="vult-shell-rail-header">
            <a
              href={homeHref}
              className="vult-shell-rail-brand vult-focus-target"
              aria-label="Vult home"
              onClick={(event) => {
                if (onNavigate && !event.metaKey && !event.ctrlKey) {
                  event.preventDefault();
                  onNavigate("dashboard");
                }
              }}
            >
              <VultBrand rail />
            </a>
          </div>
          <div className="vult-shell-rail-body">
            <div className="vult-shell-group">
              {nav()}
            </div>
          </div>
          <div className="vult-shell-rail-footer">
            <SidebarContext.Provider value={{ open: () => { if (!expanded) toggle(true); } }}>
              {sidebarCard}
            </SidebarContext.Provider>
            <div className="vult-navigation-item">{settings()}</div>
          </div>
        </div>
      </aside>
      {railTooltip.tooltip}
      <div
        className="vult-shell-rail-space"
        data-expanded={expanded}
        data-animate={animate}
        aria-hidden
      />
      <div className="shell-canvas">
        <ShellHeaderFrame
          pageShape={collection ? "collection" : undefined}
          pageIcon={
            CurrentIcon ? (
              <CurrentIcon className="shell-page-icon size-4" aria-hidden />
            ) : undefined
          }
          sidebarToggle={
            <SidebarToggleControl
              expanded={expanded}
              onToggle={toggle}
              controls="app-sidebar"
            />
          }
          mobileNavigation={
            <button
              ref={mobileTrigger}
              className="shell-icon-button vult-focus-target vult-focus-contained"
              type="button"
              aria-label="Open navigation"
              aria-expanded={mobileOpen}
              aria-controls="mobile-navigation"
              onClick={() => setMobileOpen(true)}
            >
              <Menu className="size-4" aria-hidden />
            </button>
          }
          leadingControl={
            bookSwitcher ?? (
              <span
                className="shell-book-name"
                title={bookName || "Local Book"}
              >
                {bookName || "Local Book"}
              </span>
            )
          }
          actions={
            <div className="shell-header-page-actions" ref={setHeaderActions} />
          }
          search={search}
          accountMenu={<ProfileMenu scope={userScope} />}
        >
          <nav aria-label="Breadcrumb" className="shell-breadcrumb">
            <span aria-current="page" className="shell-current-page">
              {current}
            </span>
          </nav>
        </ShellHeaderFrame>
        <main
          id="main-content"
          className="shell-scroll-region"
          data-collection={collection || undefined}
          data-workspace={active}
          tabIndex={-1}
        >
          <HeaderActionsProvider value={headerActions}>
            <FollowDisplayPreferences>{children}</FollowDisplayPreferences>
          </HeaderActionsProvider>
        </main>
      </div>
      {/* On phones the same navigation opens as a drawer that mirrors the rail:
          the Book's pages first, Settings at the bottom. */}
      <Dialog
        variant="navigation"
        title={bookName || "Your Book"}
        description={null}
        open={mobileOpen}
        onOpenChange={setMobileOpen}
        returnTo={mobileTrigger.current}
      >
        <div className="mobile-navigation" id="mobile-navigation">
          {nav(true)}
          <div className="mobile-navigation-footer">
            {sidebarCard}
            {settings(true)}
          </div>
        </div>
      </Dialog>
    </div>
  );
}
