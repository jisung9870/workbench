import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../assets/theme.js", import.meta.url), "utf8");

function loadTheme({ stored = null, storageError = false } = {}) {
  const handlers = {};
  const select = {
    value: "",
    addEventListener(name, callback) {
      handlers[name] = callback;
    },
  };
  const storage = new Map(stored === null ? [] : [["workbench.dashboard.theme.v1", stored]]);
  const localStorage = {
    getItem(key) {
      if (storageError) throw new Error("blocked");
      return storage.get(key) ?? null;
    },
    setItem(key, value) {
      if (storageError) throw new Error("blocked");
      storage.set(key, value);
    },
  };
  const window = {
    localStorage,
    addEventListener(name, callback) {
      handlers[name] = callback;
    },
  };
  const document = {
    documentElement: { dataset: {} },
    getElementById(id) {
      return id === "theme-select" ? select : null;
    },
  };
  vm.runInNewContext(source, { window, document, Set, Error });
  handlers.DOMContentLoaded();
  return { document, handlers, select, storage, window };
}

test("defaults to system and restores a valid saved preference", () => {
  const defaultTheme = loadTheme();
  assert.equal(defaultTheme.document.documentElement.dataset.theme, "system");
  assert.equal(defaultTheme.select.value, "system");

  const lightTheme = loadTheme({ stored: "light" });
  assert.equal(lightTheme.document.documentElement.dataset.theme, "light");
  assert.equal(lightTheme.select.value, "light");
});

test("persists changes and rejects damaged values", () => {
  const context = loadTheme({ stored: "neon" });
  assert.equal(context.select.value, "system");
  context.select.value = "dark";
  context.handlers.change();
  assert.equal(context.document.documentElement.dataset.theme, "dark");
  assert.equal(context.storage.get("workbench.dashboard.theme.v1"), "dark");
});

test("continues when browser storage is unavailable", () => {
  const context = loadTheme({ storageError: true });
  assert.equal(context.select.value, "system");
  context.select.value = "light";
  assert.doesNotThrow(() => context.handlers.change());
  assert.equal(context.document.documentElement.dataset.theme, "light");
});
