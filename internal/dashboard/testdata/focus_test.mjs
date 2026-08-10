import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../assets/app.js", import.meta.url), "utf8");
const start = source.indexOf("const focusDataDescriptors");
const end = source.indexOf('document.querySelectorAll("[data-page-link]")', start);
assert.ok(start >= 0 && end > start, "focus helpers are present before application startup");
const helpers = source.slice(start, end);

function datasetKey(attribute) {
  return attribute.replace(/^data-/, "").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
}

function element({ id = "", dataset = {}, tagName = "BUTTON", name = "", disabled = false, ariaDisabled = false, visible = true, hidden = false } = {}) {
  return {
    id, dataset, tagName, name, disabled, hidden, isConnected: true, parentElement: null, form: null,
    attributes: ariaDisabled ? { "aria-disabled": "true" } : {},
    focus() { this.ownerDocument.activeElement = this; },
    getAttribute(key) { return this.attributes[key] ?? null; },
    getClientRects() { return visible ? [{}] : []; },
    closest(selector) {
      if (selector === "[hidden]") {
        let current = this;
        while (current) {
          if (current.hidden) return current;
          current = current.parentElement;
        }
      }
      if (selector === "form") return this.form;
      return null;
    },
    querySelector(selector) { return selector === "summary" ? this.summary || null : null; },
  };
}

function harness(elements) {
  const notices = [];
  const body = element({ tagName: "BODY" });
  const document = {
    body,
    activeElement: body,
    elements: [],
    getElementById(id) { return this.elements.find(item => item.id === id) || null; },
    querySelectorAll(selector) {
      const match = /^\[([^\]]+)\]$/.exec(selector);
      if (!match) return [];
      const attribute = match[1];
      if (attribute === "data-route-heading") return this.elements.filter(item => Object.hasOwn(item.dataset, "routeHeading"));
      const key = datasetKey(attribute);
      return this.elements.filter(item => Object.hasOwn(item.dataset, key));
    },
  };
  body.ownerDocument = document;
  const setElements = next => {
    document.elements = next;
    next.forEach(item => { item.ownerDocument = document; });
  };
  setElements(elements);
  const context = { document, getComputedStyle: target => ({ display: target.visible === false ? "none" : "block", visibility: "visible" }), notice: message => notices.push(message), result: null };
  vm.runInNewContext(`${helpers}; result = { focusIdentityFor, findFocusTarget, focusTargetUnavailable, restoreFocus };`, context);
  return { ...context.result, document, notices, setElements };
}

test("captures and restores stable ids and existing data identities after replacement", () => {
  const heading = element({ id: "route-heading", dataset: { routeHeading: "" }, tagName: "H1" });
  const cases = [
    [element({ id: "refresh" }), element({ id: "refresh" })],
    [element({ id: "secret-submit" }), element({ id: "secret-submit" })],
    [element({ dataset: { project: "alpha" } }), element({ dataset: { project: "alpha" } })],
    [element({ dataset: { task: "task-1" } }), element({ dataset: { task: "task-1" } })],
  ];
  for (const [before, after] of cases) {
    const runtime = harness([heading, before]);
    const identity = runtime.focusIdentityFor(before);
    before.isConnected = false;
    runtime.setElements([heading, after]);
    assert.equal(runtime.restoreFocus(identity), "restored");
    assert.equal(runtime.document.activeElement, after);
    assert.deepEqual(runtime.notices, []);
  }
});

test("restores a generated form control by form id and name without label or position", () => {
  const heading = element({ id: "route-heading", dataset: { routeHeading: "" }, tagName: "H1" });
  const beforeForm = element({ id: "profile-settings-form", tagName: "FORM" });
  const before = element({ tagName: "INPUT", name: "editor" });
  before.form = beforeForm;
  const runtime = harness([heading, beforeForm, before]);
  const identity = runtime.focusIdentityFor(before);
  const afterForm = element({ id: "profile-settings-form", tagName: "FORM" });
  const after = element({ tagName: "INPUT", name: "editor" });
  after.form = afterForm;
  afterForm.elements = [after];
  before.isConnected = false;
  runtime.setElements([heading, afterForm, after]);
  assert.equal(runtime.restoreFocus(identity), "restored");
  assert.equal(runtime.document.activeElement, after);
});

test("falls back only to the visible route heading for removed, disabled, hidden, or unavailable controls", () => {
  for (const replacement of [null, element({ id: "refresh", disabled: true }), element({ id: "refresh", visible: false }), element({ id: "refresh", ariaDisabled: true })]) {
    const heading = element({ id: "route-heading", dataset: { routeHeading: "" }, tagName: "H1" });
    const destructive = element({ id: "stop-task" });
    const before = element({ id: "refresh" });
    const runtime = harness([heading, destructive, before]);
    const identity = runtime.focusIdentityFor(before);
    before.isConnected = false;
    runtime.setElements([heading, destructive, ...(replacement ? [replacement] : [])]);
    assert.equal(runtime.restoreFocus(identity), "fallback");
    assert.equal(runtime.document.activeElement, heading);
    assert.notEqual(runtime.document.activeElement, destructive);
    assert.deepEqual(runtime.notices, ["The previous control is no longer available."]);
  }
});

test("treats the focusable planned Inbox control as unavailable for automatic restoration", () => {
  const heading = element({ id: "route-heading", dataset: { routeHeading: "" }, tagName: "H1" });
  const inbox = element({ id: "inbox-planned", ariaDisabled: true });
  const runtime = harness([heading, inbox]);
  const identity = runtime.focusIdentityFor(inbox);
  assert.equal(runtime.restoreFocus(identity), "fallback");
  assert.equal(runtime.document.activeElement, heading);
});

test("does not invent identity from text, DOM order, or a Secret value", () => {
  const runtime = harness([]);
  const anonymous = element();
  anonymous.textContent = "Destructive neighbor";
  anonymous.value = "SECRET_SENTINEL";
  assert.equal(runtime.focusIdentityFor(anonymous), null);
  const descriptors = helpers.slice(0, helpers.indexOf("};") + 2);
  assert.doesNotMatch(descriptors, /\bvalue\s*:/);
});

test("wires manual, timer, success, and failure paths through the captured focus identity", () => {
  assert.match(source, /performAction\(payload, focusIdentity\)/);
  assert.match(source, /load\(\{ focusIdentity, reason: "action" \}\)/);
  assert.match(source, /restoreFocus\(focusIdentity\)/);
  assert.match(source, /focusIdentityFor\(event\.currentTarget\), reason: "manual"/);
  assert.match(source, /focusIdentityFor\(\), reason: "timer"/);
  assert.match(source, /event\.submitter \|\| document\.activeElement/);
});
