import { createRoot } from "react-dom/client";
import { Entrance, type AgentEvent, type Journey } from "../../src/components/onboarding/entrance";
import { Theme } from "../../src/design-system/theme";
import "../../src/style.css";

// Synthetic only: no requests, no owner records. Plays the first-run scene
// with a pretend agent so the whole sequence can be watched in a browser.
const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
const params = new URLSearchParams(location.search);
const start = params.get("start") === "inside" ? "inside" : "book";

const events: [number, AgentEvent][] = [
  [900, { kind: "opened", document: "checking-2026-10.pdf" }],
  [1100, { kind: "checked" }],
  [900, { kind: "account", name: "Checking" }],
  [700, { kind: "account", name: "Software" }],
  ...Array.from({ length: 12 }, (_, i): [number, AgentEvent] => [
    260,
    { kind: "entry", entry: { date: `2026-10-${String(i * 2 + 2).padStart(2, "0")}`, description: ["Client payment", "Design software", "Office rent", "Coffee supplies"][i % 4], amountMinor: String([420000, -5900, -180000, -2350][i % 4]), review: i === 7 } },
  ]),
  [600, { kind: "review", description: "Transfer to savings" }],
  [900, { kind: "compared" }],
  [900, { kind: "balanced" }],
];

const journey: Journey = {
  connection: params.has("hosted") ? { kind: "server", address: "https://books.example.com/mcp" } : { kind: "local", program: "vult" },
  waitForAgent: async () => {
    await wait(2500);
    return "Claude Code";
  },
  approveAgent: () => wait(500),
  addStatement: () => wait(900),
  watchAgent: async (onEvent) => {
    for (const [delay, event] of events) {
      await wait(delay);
      onEvent(event);
    }
  },
};

createRoot(document.getElementById("root")!).render(
  <Theme initialPreference="system" initialTheme="light">
  <Entrance
    start={start}
    createBook={() => wait(700)}
    journey={journey}
    onOpen={(page) => alert(`Opens the ${page}`)}
  />
  </Theme>,
);
