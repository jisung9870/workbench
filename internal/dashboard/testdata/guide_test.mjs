import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../assets/guide.js", import.meta.url), "utf8");

function createLink(id) {
  const attributes = new Map([["href", `#${id}`]]);
  const classes = new Set();
  return {
    hidden: false,
    classList: {
      toggle(name, active) {
        if (active) classes.add(name);
        else classes.delete(name);
      },
      contains(name) {
        return classes.has(name);
      },
    },
    getAttribute(name) {
      return attributes.get(name) ?? null;
    },
    setAttribute(name, value) {
      attributes.set(name, value);
    },
    removeAttribute(name) {
      attributes.delete(name);
    },
  };
}

function loadGuide() {
  const handlers = {};
  const search = {
    value: "",
    blurred: false,
    addEventListener(name, callback) {
      handlers[name] = callback;
    },
    blur() {
      this.blurred = true;
    },
  };
  const empty = { hidden: true };
  const sections = [
    { id: "overview", textContent: "Workbench overview", hidden: false },
    { id: "worktrees", textContent: "Git worktree lifecycle", hidden: false },
    { id: "cli-reference", textContent: "CLI commands", hidden: false },
  ];
  const links = sections.map((section) => createLink(section.id));
  const targets = new Map(sections.map((section) => [`#${section.id}`, section]));
  let observer;
  class IntersectionObserver {
    constructor(callback) {
      this.callback = callback;
      this.observed = [];
      observer = this;
    }
    observe(section) {
      this.observed.push(section);
    }
  }
  const document = {
    getElementById(id) {
      if (id === "guide-search") return search;
      if (id === "guide-search-empty") return empty;
      return null;
    },
    querySelector(selector) {
      return targets.get(selector) ?? null;
    },
    querySelectorAll(selector) {
      if (selector === "[data-guide-link]") return links;
      if (selector === "[data-searchable]" || selector === "main.guide-content section[id]") return sections;
      return [];
    },
  };
  const window = { IntersectionObserver };
  vm.runInNewContext(source, { document, window, IntersectionObserver, String, Array, Boolean });
  return { empty, handlers, links, observer, search, sections };
}

test("filters sections and their navigation links", () => {
  const guide = loadGuide();
  guide.search.value = "worktree";
  guide.handlers.input();

  assert.deepEqual(guide.sections.map((section) => section.hidden), [true, false, true]);
  assert.deepEqual(guide.links.map((link) => link.hidden), [true, false, true]);
  assert.equal(guide.empty.hidden, true);

  guide.search.value = "missing";
  guide.handlers.input();
  assert.equal(guide.empty.hidden, false);
});

test("Escape clears search and restores all sections", () => {
  const guide = loadGuide();
  guide.search.value = "worktree";
  guide.handlers.input();
  guide.handlers.keydown({ key: "Escape" });

  assert.equal(guide.search.value, "");
  assert.equal(guide.search.blurred, true);
  assert.ok(guide.sections.every((section) => !section.hidden));
  assert.ok(guide.links.every((link) => !link.hidden));
  assert.equal(guide.empty.hidden, true);
});

test("marks the intersecting section link as current", () => {
  const guide = loadGuide();
  assert.equal(guide.observer.observed.length, guide.sections.length);
  guide.observer.callback([
    { isIntersecting: true, boundingClientRect: { top: 40 }, target: guide.sections[1] },
  ]);

  assert.equal(guide.links[1].classList.contains("active"), true);
  assert.equal(guide.links[1].getAttribute("aria-current"), "location");
  assert.equal(guide.links[0].classList.contains("active"), false);
  assert.equal(guide.links[0].getAttribute("aria-current"), null);
});
