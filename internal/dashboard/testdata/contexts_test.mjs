import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../assets/app.js", import.meta.url), "utf8");
const start = source.indexOf("function renderContexts");
const end = source.indexOf("function renderOverview", start);
assert.ok(start >= 0 && end > start, "renderContexts function is present");
const renderer = source.slice(start, end);

const esc = value => String(value ?? "").replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);

function render(contexts, project = { id: "alpha", environment_id: "dev" }) {
  const elements = {
    "context-status": { textContent: "", className: "" },
    contexts: { innerHTML: "" },
  };
  const sandbox = { $, esc };
  function $(id) { return elements[id]; }
  vm.runInNewContext(`${renderer}; renderContexts(input, project);`, { ...sandbox, input: contexts, project });
  return elements;
}

test("renders only allowlisted context metadata", () => {
  const elements = render({
    registry_available: true,
    environments: [{
      id: "dev", aws_profile: "sandbox", aws_region: "ap-northeast-2",
      kube_context: "local", kube_namespace: "tools", export_keys: ["FEATURE"],
      secret_references: [{ variable: "TOKEN", service: "private-service", field: "raw-field", reference: "sec://must-not-render/value", value: "SECRET_SENTINEL", status: "available" }],
    }],
  });
  const html = elements.contexts.innerHTML;
  for (const expected of ["dev", "sandbox", "ap-northeast-2", "local", "tools", "FEATURE", "TOKEN", "available"]) assert.match(html, new RegExp(expected));
  for (const forbidden of ["private-service", "raw-field", "sec://must-not-render/value", "SECRET_SENTINEL"]) assert.doesNotMatch(html, new RegExp(forbidden));
  assert.equal(elements["context-status"].textContent, "available");
});

test("distinguishes unavailable, unlinked, missing, and unhealthy references", () => {
  let elements = render({ registry_available: false, reason: "registry unreadable", environments: [] });
  assert.equal(elements["context-status"].textContent, "unavailable");
  assert.match(elements.contexts.innerHTML, /registry unreadable/);

  elements = render({ registry_available: true, environments: [] }, { id: "alpha" });
  assert.equal(elements["context-status"].textContent, "not linked");

  elements = render({ registry_available: true, environments: [] });
  assert.equal(elements["context-status"].textContent, "missing");

  elements = render({ registry_available: true, environments: [{ id: "dev", export_keys: [], secret_references: [{ variable: "TOKEN", status: "invalid_reference" }] }] });
  assert.equal(elements["context-status"].textContent, "review");
  assert.match(elements.contexts.innerHTML, /invalid_reference/);
});

test("escapes every context field before inserting markup", () => {
  const payloads = {
    id: `<img src=x onerror="idAttack()">`,
    awsProfile: `profile"><img src=x onerror='profileAttack()'>`,
    awsRegion: `region' onmouseover='regionAttack()`,
    kubeContext: `<img src=x onerror="contextAttack()">`,
    kubeNamespace: `namespace" autofocus onfocus="namespaceAttack()`,
    exportKey: `<img src=x onerror="exportAttack()">`,
    variable: `TOKEN"><img src=x onerror="variableAttack()">`,
    status: `available" onmouseover="statusAttack()`,
  };
  const elements = render({
    registry_available: true,
    environments: [{
      id: payloads.id,
      aws_profile: payloads.awsProfile,
      aws_region: payloads.awsRegion,
      kube_context: payloads.kubeContext,
      kube_namespace: payloads.kubeNamespace,
      export_keys: [payloads.exportKey],
      secret_references: [{ variable: payloads.variable, status: payloads.status }],
    }],
  }, { id: "alpha", environment_id: payloads.id });
  const html = elements.contexts.innerHTML;
  for (const payload of Object.values(payloads)) {
    assert.ok(html.includes(esc(payload)), `escaped payload missing: ${payload}`);
  }
  for (const executable of ["<img", `onerror="`, `onerror='`, ` onmouseover="`, ` onmouseover='`, ` autofocus onfocus="`]) {
    assert.equal(html.includes(executable), false, `executable markup or attribute survived: ${executable}`);
  }
  assert.equal((html.match(/<img/gu) || []).length, 0);
});
