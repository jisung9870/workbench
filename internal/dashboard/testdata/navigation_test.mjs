import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../assets/index.html", import.meta.url), "utf8");
const app = await readFile(new URL("../assets/app.js", import.meta.url), "utf8");
const style = await readFile(new URL("../assets/style.css", import.meta.url), "utf8");
const navStart = html.indexOf('<nav class="product-nav" aria-label="Workbench areas">');
const navEnd = html.indexOf("</nav>", navStart);
assert.ok(navStart >= 0 && navEnd > navStart, "six-area navigation is present");
const nav = html.slice(navStart, navEnd);

test("shows the six target labels in order while using only current destinations", () => {
  const labels = ["Today", "Inbox", "Projects", "Runs &amp; Agents", "Integrations", "System &amp; Recovery"];
  let previous = -1;
  for (const label of labels) {
    const position = nav.indexOf(label);
    assert.ok(position > previous, `${label} follows the preceding target area`);
    previous = position;
  }
  for (const [page, href] of [["dashboard", "/"], ["projects", "/projects"], ["activity", "/activity"], ["settings", "/settings"], ["system", "/system"]]) {
    assert.match(nav, new RegExp(`data-page-link="${page}" href="${href.replace("/", "\\/")}"`));
  }
  for (const target of ["/today", "/inbox", "/runs", "/integrations"]) assert.doesNotMatch(html, new RegExp(`href="${target}"`));
});

test("keeps Inbox focusable but non-navigating and explicitly S2-planned", () => {
  const inbox = /<button id="inbox-planned"[^>]*>([\s\S]*?)<\/button>/.exec(nav);
  assert.ok(inbox, "Inbox is a button, not a link");
  assert.match(inbox[0], /aria-disabled="true"/);
  assert.doesNotMatch(inbox[0], /\sdisabled(?:\s|=|>)/);
  assert.doesNotMatch(inbox[0], /href=/);
  assert.match(html, /Planned and unavailable; requires S2\./);
  assert.match(app, /Inbox is planned and unavailable; it requires S2\./);
});

test("keeps Guide outside the six primary areas and maps active current pages", () => {
  assert.doesNotMatch(nav, />Guide</);
  assert.match(html.slice(navEnd), /<nav class="help-nav" aria-label="Help"><a id="guide-link" href="\/guide">Guide<\/a><\/nav>/);
  assert.match(app, /setAttribute\("aria-current", "page"\)/);
});

test("provides a focusable route heading and fixed persistent failure summary", () => {
  assert.equal((html.match(/data-route-heading/g) || []).length, 3);
  assert.equal((html.match(/data-route-heading tabindex="-1"/g) || []).length, 3);
  assert.match(html, /id="failure-summary"[^>]*role="alert"[^>]*tabindex="-1"[^>]*hidden/);
  assert.match(app, /safeErrorCode/);
  assert.doesNotMatch(app, /body\.error\?\.message/);
  assert.doesNotMatch(app, /Snapshot failed: \$\{error\.message\}/);
});

test("keeps the narrow compatibility shell reflowable without hiding planned semantics", () => {
  assert.match(style, /@media \(max-width: 720px\)[\s\S]*min-height: 44px/);
  assert.match(style, /body, button, input, select \{ font-size: 16px; \}/);
  assert.match(style, /\.product-nav \{ flex: 1; \}/);
  assert.match(style, /\.snapshot-generated \{ width: 100%; \}/);
});
