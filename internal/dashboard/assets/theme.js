(function () {
  "use strict";

  const storageKey = "workbench.dashboard.theme.v1";
  const allowed = new Set(["system", "light", "dark"]);

  function readPreference() {
    try {
      const stored = window.localStorage.getItem(storageKey);
      return allowed.has(stored) ? stored : "system";
    } catch (_) {
      return "system";
    }
  }

  function applyPreference(preference) {
    const safePreference = allowed.has(preference) ? preference : "system";
    document.documentElement.dataset.theme = safePreference;
    return safePreference;
  }

  function savePreference(preference) {
    const safePreference = applyPreference(preference);
    try {
      window.localStorage.setItem(storageKey, safePreference);
    } catch (_) {
      // Theme persistence is optional; the selected theme still applies to this page.
    }
    return safePreference;
  }

  const initialPreference = applyPreference(readPreference());

  window.addEventListener("DOMContentLoaded", function () {
    const select = document.getElementById("theme-select");
    if (!select) return;
    select.value = initialPreference;
    select.addEventListener("change", function () {
      select.value = savePreference(select.value);
    });
  });

  window.WorkbenchTheme = { apply: applyPreference, read: readPreference, save: savePreference };
})();
