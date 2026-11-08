import {
  useContext,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { Tab, TabList, TabListStateContext } from "react-aria-components";
import "./tab-strip.css";

export type TabStripItem = {
  id: string;
  label: ReactNode;
  isDisabled?: boolean;
};

/** Tab standard v1 (docs/design/tabs.md): the one tab strip. The parent Tabs
 * owns panels and selection; screens never restyle the strip or its tabs. */
export function TabStrip({
  items,
  "aria-label": ariaLabel,
}: {
  items: readonly TabStripItem[];
  "aria-label": string;
}) {
  const state = useContext(TabListStateContext);
  const index = items.findIndex((item) => item.id === state?.selectedKey);
  const style = {
    "--vult-tab-count": Math.max(items.length, 1),
    "--vult-active-tab": Math.max(index, 0),
  } as CSSProperties;

  return (
    <div className="vult-tab-strip-scroll">
      <TabList
        aria-label={ariaLabel}
        className="vult-tab-strip"
        style={style}
        data-selection-visible={index >= 0}
      >
        {items.map((item) => (
          <Tab
            key={item.id}
            id={item.id}
            isDisabled={item.isDisabled}
            className="vult-tab-strip-item vult-type-label"
          >
            {item.label}
          </Tab>
        ))}
      </TabList>
    </div>
  );
}
