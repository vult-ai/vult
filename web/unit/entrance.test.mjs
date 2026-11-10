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
    assert.doesNotMatch(document.body.textContent, /Create a passkey|Save recovery codes|authenticator app/);

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
    assert.doesNotMatch(document.body.textContent, /Create a passkey|Save recovery codes|authenticator app/);
    await click("Open Example Book");
    assert.deepEqual(opened, ["dashboard"]);
  });
});

test("waiting for an agent never traps the owner outside the Book", async () => {
  await withDOM(async ({ render }) => {
    const opened = [];
    await render(React.createElement(Entrance, {
      start: "inside",
      book: { name: "Example Book", profile: "personal", currency: "USD" },
      createBook: unreachable,
      journey: journey({
        connection: { kind: "server", address: "https://mcp.example.test" },
        waitForAgent: () => new Promise(() => {}),
        resume: async () => ({ file: "", skipped: true, allowed: false }),
      }),
      onOpen: (page) => opened.push(page),
    }));
    assert.match(document.querySelector(".entrance-journey").textContent, /Connect an agent/);
    await click("Skip for now");
    assert.deepEqual(opened, ["dashboard"]);
  });
});

test("no later step of the entrance keeps the owner outside the Book", async () => {
  const cases = [
    ["an agent that hasn't started", { file: "synthetic-statement.pdf", agent: "Example agent", allowed: true }, () => new Promise(() => {}), /Waiting for Example agent to start/],
    ["an agent waiting to be let in", { file: "synthetic-statement.pdf", agent: "Example agent", allowed: false }, unreachable, /Example agent wants to come in/],
    ["an agent at work", { file: "synthetic-statement.pdf", agent: "Example agent", allowed: true }, (emit) => { emit({ kind: "opened", document: "synthetic-statement.pdf" }); return new Promise(() => {}); }, /Reading synthetic-statement.pdf/],
    ["a statement the agent couldn't use", { file: "synthetic-statement.pdf", agent: "Example agent", allowed: true }, async (emit) => { emit({ kind: "unusable", reason: "Example unreadable page" }); }, /couldn’t use synthetic-statement.pdf/],
  ];
  for (const [name, where, watchAgent, shown] of cases) {
    await withDOM(async ({ render }) => {
      const opened = [];
      await render(React.createElement(Entrance, {
        start: "inside",
        book: { name: "Example Book", profile: "personal", currency: "USD" },
        createBook: unreachable,
        journey: journey({ resume: async () => where, watchAgent }),
        onOpen: (page) => opened.push(page),
      }));
      await React.act(async () => {});
      assert.match(document.querySelector(".entrance-journey").textContent, shown, name);
      await click("Skip for now");
      assert.deepEqual(opened, ["dashboard"], name);
    });
  }
});

test("a statement that fails to upload says so and stays on the step", async () => {
  await withDOM(async ({ render }) => {
    await render(React.createElement(Entrance, {
      start: "inside",
      book: { name: "Example Book", profile: "personal", currency: "USD" },
      createBook: unreachable,
      journey: journey({
        addStatement: async () => {
          throw new Error("The server refused the file.");
        },
      }),
    }));
    const input = document.querySelector(".entrance-drop input[type=file]");
    const file = new File(["%PDF-1.4"], "example-statement.pdf", { type: "application/pdf" });
    Object.defineProperty(input, "files", { value: [file] });
    await React.act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    const alert = document.querySelector(".entrance-error[role=alert]");
    assert.ok(alert, "the failure is announced");
    assert.match(alert.textContent, /example-statement\.pdf didn’t upload\. The server refused the file\./);
    assert.match(document.querySelector(".entrance-journey").textContent, /Start with a statement/);
  });
});

test("a Book that was never named starts at naming it, with its currency fixed", async () => {
  await withDOM(async ({ render }) => {
    const named = [];
    await render(React.createElement(Entrance, {
      start: "book",
      unnamed: { currency: "EUR" },
      createBook: async (book) => {
        named.push(book);
      },
      journey: journey(),
    }));
    const form = document.querySelector(".entrance-book-form");
    assert.equal(form.hasAttribute("inert"), false);
    const nameInput = [...form.querySelectorAll("input")].find((i) => i.getAttribute("placeholder") !== null && !i.readOnly);
    assert.equal(nameInput.value, "", "the placeholder name isn't offered as the name");
    const currency = [...form.querySelectorAll("input")].find((i) => i.readOnly);
    assert.equal(currency.value, "EUR");
    assert.ok(button("Continue"));
    assert.equal(button("Create Book"), undefined);
  });
});
