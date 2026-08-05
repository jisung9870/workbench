(function () {
  "use strict";

  const search = document.getElementById("guide-search");
  const links = Array.from(document.querySelectorAll("[data-guide-link]"));
  const searchable = Array.from(document.querySelectorAll("[data-searchable]"));
  const empty = document.getElementById("guide-search-empty");

  function normalize(value) {
    return String(value || "").toLocaleLowerCase().replace(/\s+/g, " ").trim();
  }

  function filterGuide() {
    const query = normalize(search && search.value);
    let matches = 0;
    searchable.forEach(function (item) {
      const visible = !query || normalize(item.textContent).includes(query);
      item.hidden = !visible;
      if (visible) matches += 1;
    });
    links.forEach(function (link) {
      const target = document.querySelector(link.getAttribute("href"));
      link.hidden = Boolean(query && target && target.hidden);
    });
    if (empty) empty.hidden = matches > 0;
  }

  if (search) {
    search.addEventListener("input", filterGuide);
    search.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        search.value = "";
        filterGuide();
        search.blur();
      }
    });
  }

  const sections = Array.from(document.querySelectorAll("main.guide-content section[id]"));
  if ("IntersectionObserver" in window) {
    const observer = new IntersectionObserver(function (entries) {
      const current = entries
        .filter(function (entry) { return entry.isIntersecting; })
        .sort(function (left, right) { return left.boundingClientRect.top - right.boundingClientRect.top; })[0];
      if (!current) return;
      links.forEach(function (link) {
        const active = link.getAttribute("href") === "#" + current.target.id;
        link.classList.toggle("active", active);
        if (active) link.setAttribute("aria-current", "location");
        else link.removeAttribute("aria-current");
      });
    }, { rootMargin: "-15% 0px -70% 0px" });
    sections.forEach(function (section) { observer.observe(section); });
  }
})();
