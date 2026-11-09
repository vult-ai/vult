import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { Entrance: Scene, Theme } = await loadComponent("unit/fixtures/entrance-in-theme.tsx");
const Entrance = (props) => React.createElement(Theme, { initialTheme: "light", initialPreference: "light" }, React.createElement(Scene, props));

const button = (label) => [...document.querySelectorAll("button")].find((node) => node.textContent.trim() === label);
const click = async (label) => {
  const target = button(label);
  assert.ok(target, `the ${label} action is available`);
  await React.act(async () => target.click());
};
const unreachable = async () => assert.fail("this step should not run");
const journey = (steps = {}) => ({
  connection: { kind: "local", program: "vult" },
  waitForAgent: unreachable,
  approveAgent: unreachable,
  addStatement: unreachable,
  watchAgent: unreachable,
  ...steps,
});

test("a new Book opens its statement and agent steps only after creation succeeds", async () => {
  await withDOM(async ({ render }) => {
    let finishCreation;
    let agentArrives;
    const created = [];
    const opened = [];
    let skipped = 0;
    let approved = 0;
    await render(React.createElement(Entrance, {
      draft: { name: "  Example Book  ", profile: "personal", currency: "USD", timezone: "UTC" },
      createBook: (book) => {
        created.push(book);
        return new Promise((resolve) => { finishCreation = resolve; });
      },
      journey: journey({
        skipStatement: () => { skipped += 1; },
        waitForAgent: () => new Promise((resolve) => { agentArrives = resolve; }),
        approveAgent: async () => { approved += 1; },
      }),
      onOpen: (page) => opened.push(page),
    }));
    const form = document.querySelector(".entrance-book-form");
    assert.ok(button("Create Book"));
    assert.equal(form.hasAttribute("inert"), false);
    assert.equal(document.querySelector(".entrance-journey"), null);
    assert.doesNotMatch(document.body.textContent, /Create your passkey|Your recovery codes|authenticator app/);

    await click("Create Book");
    assert.deepEqual(created, [{ name: "Example Book", profile: "personal", currency: "USD", timezone: "UTC" }]);
    assert.equal(document.querySelector(".entrance-journey"), null, "creation is still pending");
    await React.act(async () => finishCreation());
    assert.equal(form.hasAttribute("inert"), true, "the departed form cannot receive keyboard input");
    assert.match(document.querySelector(".entrance-journey").textContent, /Start with a statement/);

    await click("Skip for now");
    assert.equal(skipped, 1);
    assert.equal(document.querySelector(".entrance-copy code").textContent, "vult mcp install");
    await React.act(async () => agentArrives("Example agent"));
    assert.match(document.querySelector(".entrance-journey").textContent, /Example agent wants to come in/);
    await click("Allow");
    assert.equal(approved, 1);
    await click("Open Example Book");
    assert.deepEqual(opened, ["dashboard"]);
  });
});

test("an existing Book resumes inside without recreating it or repeating sign-in", async () => {
  await withDOM(async ({ render }) => {
    const opened = [];
    let resumed = 0;
    await render(React.createElement(Entrance, {
      start: "inside",
      book: { name: "Example Book", profile: "business", currency: "EUR" },
      createBook: unreachable,
      journey: journey({
        resume: async () => {
          resumed += 1;
          return { file: "", skipped: true, agent: "Example agent", allowed: true };
        },
      }),
      onOpen: (page) => opened.push(page),
    }));
    assert.equal(resumed, 1);
    assert.equal(document.querySelector(".entrance-book-form").hasAttribute("inert"), true);
    assert.match(document.querySelector(".entrance-journey").textContent, /Example Book is ready/);
    assert.doesNotMatch(document.body.textContent, /Create your passkey|Your recovery codes|authenticator app/);
    await click("Open Example Book");
    assert.deepEqual(opened, ["dashboard"]);
  });
});
