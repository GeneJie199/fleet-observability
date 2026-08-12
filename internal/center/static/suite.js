(() => {
  "use strict";

  const host = location.hostname.includes(":") ? `[${location.hostname}]` : location.hostname;
  const root = `${location.protocol}//${host}`;
  const defaults = {
    devcycle: `${root}:8766/`,
    infrascout: `${root}:8765/`,
    fleetscope: `${root}:8770/`,
    releaseguard: `${root}:8771/`,
  };
  const params = new URLSearchParams(location.search);
  const keys = Object.keys(defaults);
  const overrides = Object.fromEntries(keys.map((key) => [key, params.get(`suite-${key}`) || ""]));
  const currentKey = document.querySelector(".suite-menu a.current")?.dataset.suite;
  if (currentKey && !overrides[currentKey]) {
    const currentBase = new URL(".", location.href);
    currentBase.search = "";
    currentBase.hash = "";
    overrides[currentKey] = currentBase.href;
  }

  function configureSuiteLinks(rootNode = document) {
    rootNode.querySelectorAll("[data-suite]").forEach((link) => {
      const key = link.dataset.suite;
      const target = new URL(overrides[key] || defaults[key], location.href);
      keys.forEach((name) => {
        if (overrides[name]) target.searchParams.set(`suite-${name}`, overrides[name]);
      });
      link.href = target.href;
      if (link.classList.contains("current")) link.setAttribute("aria-current", "page");
    });
  }
  configureSuiteLinks();
  new MutationObserver((records) => records.forEach((record) => record.addedNodes.forEach((node) => {
    if (node.nodeType === Node.ELEMENT_NODE) configureSuiteLinks(node);
  }))).observe(document.body, { childList: true, subtree: true });

  document.addEventListener("click", (event) => {
    document.querySelectorAll(".suite-switcher[open]").forEach((menu) => {
      if (!menu.contains(event.target)) menu.removeAttribute("open");
    });
  });
})();
