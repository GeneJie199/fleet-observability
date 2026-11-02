/* Shared presentation only. Product code owns data, permissions and navigation. */
(() => {
  "use strict";
  const key = "ai-devops.ui.theme.v2";
  const modes = new Set(["light", "dark", "system"]);
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  const mobile = window.matchMedia("(max-width: 760px)");
  let preferred = "system";
  let query = null;
  try {
    query = new URL(location.href).searchParams.get("suite-theme");
  } catch (_) { /* Embedded previews may not have a normal HTTP URL. */ }
  if (modes.has(query)) {
    preferred = query;
    try { localStorage.setItem(key, query); } catch (_) { /* The explicit URL still wins without storage. */ }
  } else {
    try {
      const saved = localStorage.getItem(key);
      if (modes.has(saved)) preferred = saved;
    } catch (_) { /* Private browsing must not disable the application. */ }
  }
  function resolve() { return preferred === "system" ? (system.matches ? "dark" : "light") : preferred; }
  function applyTheme() {
    document.documentElement.dataset.suiteTheme = resolve();
    document.documentElement.dataset.suiteThemeMode = preferred;
    document.querySelectorAll("[data-suite-theme-select]").forEach((el) => { el.value = preferred; });
    window.dispatchEvent(new CustomEvent("suite:themechange", { detail: { mode: preferred, resolved: resolve() } }));
  }
  applyTheme();
  const pagePaths = { suite: "suite.html", team: "team.html", providers: "team.html#providers", delivery: "team.html#missions" };
  function decorateLink(link) {
    const href = link.getAttribute("href");
    if (!href || href.startsWith("#")) return;
    let target;
    try { target = new URL(href, location.href); } catch (_) { return; }
    if (target.protocol !== "http:" && target.protocol !== "https:") return;
    const page = link.dataset.suitePage;
    if (Object.hasOwn(pagePaths, page || "")) {
      const base = new URL("/", target);
      const route = new URL(pagePaths[page], base);
      target.pathname = route.pathname;
      target.hash = route.hash;
    }
    target.searchParams.set("suite-theme", preferred);
    const next = target.href;
    if (link.href !== next) link.href = next;
  }
  const selector = "a[data-suite],a[data-module-port],a[data-suite-page],a[data-ui-link]";
  function refreshNavigation(root = document) {
    if (root.matches?.(selector)) decorateLink(root);
    root.querySelectorAll?.(selector).forEach(decorateLink);
  }
  function setTheme(mode) {
    if (!modes.has(mode)) return;
    preferred = mode;
    try { localStorage.setItem(key, mode); } catch (_) { /* Optional storage. */ }
    try {
      const current = new URL(location.href);
      current.searchParams.set("suite-theme", mode);
      history.replaceState(history.state, "", current);
    } catch (_) { /* Embedded or file-based previews may deny history writes. */ }
    applyTheme();
    refreshNavigation();
  }
  system.addEventListener("change", () => { if (preferred === "system") applyTheme(); });
  Object.defineProperty(window, "SuiteUI", { value: Object.freeze({ setTheme, getTheme: () => preferred, refreshNavigation }), writable: false });
  function initialize() {
    const body = document.body;
    const header = document.querySelector("[data-ui-header]") || document.querySelector("body > header.topbar,body > header");
    const navSelector = body.dataset.suiteNav;
    let nav = null;
    if (navSelector) {
      try { nav = document.querySelector(navSelector); } catch (_) { /* Trusted markup typo has no runtime authority. */ }
    }
    nav ||= document.querySelector("[data-ui-nav]");
    const main = document.querySelector("[data-ui-main]") || document.querySelector("main");
    if (header) { header.classList.add("suite-topbar"); body.classList.add("suite-has-header"); }
    if (nav) {
      nav.classList.add("suite-side-nav");
      if (!nav.id) nav.id = "suite-product-nav";
      body.classList.add("suite-has-sidebar");
      if (main) main.classList.add("suite-main");
    }
    if (header && !header.querySelector(".suite-ui-tools")) {
      const tools = document.createElement("div");
      tools.className = "suite-ui-tools";
      const label = document.createElement("label");
      label.className = "suite-theme-label";
      const text = document.createElement("span");
      text.textContent = "主题";
      const theme = document.createElement("select");
      theme.dataset.suiteThemeSelect = "";
      theme.setAttribute("aria-label", "外观 / Appearance");
      for (const [value, title] of [["light", "浅色 · Light"], ["dark", "深色 · Dark"], ["system", "随系统 · Auto"]]) {
        const option = document.createElement("option"); option.value = value; option.textContent = title; theme.append(option);
      }
      theme.value = preferred;
      theme.addEventListener("change", () => setTheme(theme.value));
      label.append(text, theme); tools.append(label);
      if (nav) {
        const toggle = document.createElement("button");
        toggle.type = "button"; toggle.className = "suite-menu-toggle";
        toggle.setAttribute("aria-label", "打开导航 / Open navigation");
        toggle.setAttribute("aria-controls", nav.id); toggle.setAttribute("aria-expanded", "false");
        toggle.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"/></svg>';
        function closeMenu(restoreFocus = false) {
          body.classList.remove("suite-nav-open"); toggle.setAttribute("aria-expanded", "false");
          if (restoreFocus) toggle.focus();
        }
        toggle.addEventListener("click", () => {
          const open = body.classList.toggle("suite-nav-open");
          toggle.setAttribute("aria-expanded", String(open));
          if (open) nav.querySelector("a,button")?.focus();
        });
        nav.addEventListener("click", (event) => {
          if (!mobile.matches || !event.target.closest("a,button")) return;
          closeMenu();
          if (main && nav.contains(document.activeElement)) {
            if (!main.hasAttribute("tabindex")) main.tabIndex = -1;
            main.focus({ preventScroll: true });
          }
        });
        document.addEventListener("keydown", (event) => {
          if (event.key === "Escape" && body.classList.contains("suite-nav-open")) { closeMenu(true); event.preventDefault(); }
        });
        document.addEventListener("click", (event) => {
          if (mobile.matches && !nav.contains(event.target) && !toggle.contains(event.target)) closeMenu();
        });
        mobile.addEventListener("change", () => closeMenu());
        tools.prepend(toggle);
      }
      header.append(tools);
    }
    if (header) {
      const fitHeader = () => {
        const height = header.getBoundingClientRect().height;
        if (height > 0 && Number.isFinite(height)) {
          document.documentElement.style.setProperty("--ui-header-space", `${height}px`);
        }
      };
      fitHeader();
      if (typeof ResizeObserver === "function") new ResizeObserver(fitHeader).observe(header);
      else window.addEventListener("resize", fitHeader);
    }
    refreshNavigation();
    // Existing module discovery may resolve sibling ports after page load.
    // Keep its URL and Suite context, adding presentation state only.
    new MutationObserver((records) => {
      for (const record of records) {
        if (record.type === "attributes" && record.target.matches(selector)) decorateLink(record.target);
        for (const node of record.addedNodes) if (node.nodeType === 1) refreshNavigation(node);
      }
    }).observe(body, { childList: true, subtree: true, attributes: true, attributeFilter: ["href"] });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", initialize, { once: true });
  else initialize();
})();
