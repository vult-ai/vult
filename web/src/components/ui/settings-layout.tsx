import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { TabPanel, Tabs } from "react-aria-components";
import { TabStrip } from "./tab-strip";
import "./settings-section.css";
import "./settings-layout.css";

type SettingsTab = {
  id: string;
  label: string;
  content: ReactNode | ((active: boolean) => ReactNode);
  visible?: boolean;
  eager?: boolean;
};

export function SettingsLayout({
  tabs,
  active = true,
}: {
  tabs: SettingsTab[];
  active?: boolean;
}) {
  // A link may open any tab by its id (?tab=data); otherwise the first tab.
  const requested = new URLSearchParams(window.location.search).get("tab");
  const initialTab =
    tabs.find((tab) => tab.id === requested)?.id ?? tabs[0]?.id ?? "book";
  const [selected, setSelected] = useState(initialTab);
  const [visited, setVisited] = useState(() => new Set([initialTab]));
  const canvas = useRef<HTMLDivElement>(null);
  const visibleTabs = tabs.filter((tab) => tab.visible !== false);
  const visibleTabIDs = visibleTabs.map((tab) => tab.id).join(" ");
  useLayoutEffect(() => {
    const viewport = canvas.current?.querySelector<HTMLElement>(
      ".vult-tab-strip-scroll",
    );
    if (!viewport) return;
    const reveal = () => {
      const selectedTab = viewport.querySelector<HTMLElement>(
        '[role="tab"][aria-selected="true"]',
      );
      if (!selectedTab) return;
      const frame = viewport.getBoundingClientRect();
      const tab = selectedTab.getBoundingClientRect();
      if (tab.right > frame.right - 4)
        viewport.scrollLeft += tab.right - frame.right + 4;
      else if (tab.left < frame.left + 4)
        viewport.scrollLeft -= frame.left - tab.left + 4;
    };
    const frame = requestAnimationFrame(reveal);
    window.addEventListener("resize", reveal);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", reveal);
    };
  }, [selected, visibleTabIDs]);
  return (
    <div
      ref={canvas}
      className="workspace-page book-settings-page settings-page settings-canvas"
    >
      {/* The page bar names the page; the page starts with its tabs. */}
      <Tabs
        selectedKey={selected}
        onSelectionChange={(key) => {
          setSelected(String(key));
          setVisited((previous) => new Set([...previous, String(key)]));
        }}
        className="book-settings-tabs"
      >
        <TabStrip
          aria-label="Settings"
          items={visibleTabs}
        />
        {tabs.map((tab) => (
          <TabPanel
            key={tab.id}
            id={tab.id}
            shouldForceMount
            className="book-settings-tab-panel"
          >
            {(visited.has(tab.id) || tab.eager) &&
              (typeof tab.content === "function"
                ? tab.content(active && selected === tab.id)
                : tab.content)}
          </TabPanel>
        ))}
      </Tabs>
    </div>
  );
}
