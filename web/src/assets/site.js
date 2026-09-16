const copyText = async (value) => {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }

  const input = document.createElement("textarea");
  input.value = value;
  input.setAttribute("readonly", "");
  input.style.position = "fixed";
  input.style.opacity = "0";
  document.body.append(input);
  input.select();
  document.execCommand("copy");
  input.remove();
};

const showToast = (message) => {
  const region = document.querySelector("[data-toast-region]");
  if (!region) return;
  const toast = document.createElement("div");
  toast.className = "mp-toast mp-toast--success";
  toast.textContent = message;
  region.append(toast);
  window.setTimeout(() => toast.remove(), 2200);
};

const setTemporaryLabel = (button, label, temporary) => {
  label.textContent = temporary;
  window.setTimeout(() => { label.textContent = button.dataset.originalLabel || "Copy"; }, 1800);
};

document.querySelectorAll("[data-copy-markdown]").forEach((button) => {
  const label = button.querySelector("[data-copy-label]");
  if (!label) return;
  button.dataset.originalLabel = label.textContent;
  button.addEventListener("click", async () => {
    try {
      const response = await fetch(button.dataset.rawUrl);
      if (!response.ok) throw new Error("Markdown source is unavailable");
      const source = await response.text();
      const body = source
        .replace(/^\uFEFF?---\r?\n[\s\S]*?\r?\n---\r?\n?/, "")
        .trim();
      const markdown = `# ${button.dataset.title}\n\n${button.dataset.description}\n\n${body}\n`;
      await copyText(markdown);
      setTemporaryLabel(button, label, "Copied MD");
      showToast("Markdown copied.");
    } catch {
      setTemporaryLabel(button, label, "Copy failed");
      showToast("Markdown could not be copied.");
    }
  });
});

document.querySelectorAll("[data-copy-code]").forEach((button) => {
  const label = button.querySelector("span");
  button.dataset.originalLabel = label?.textContent || "Copy";
  button.addEventListener("click", async () => {
    const code = button.closest("[data-code-block]")?.querySelector("code")?.textContent;
    if (!code || !label) return;
    await copyText(code);
    setTemporaryLabel(button, label, "Copied");
    showToast("Code copied.");
  });
});

document.querySelectorAll("[data-copy-value]").forEach((button) => {
  const label = button.querySelector("span");
  button.dataset.originalLabel = label?.textContent || "Copy";
  button.addEventListener("click", async () => {
    if (!label) return;
    await copyText(button.dataset.copyValue || "");
    setTemporaryLabel(button, label, "Copied");
    showToast("Command copied.");
  });
});

const navToggle = document.querySelector("[data-nav-toggle]");
const topNav = document.querySelector("[data-top-nav]");
navToggle?.addEventListener("click", () => {
  const expanded = navToggle.getAttribute("aria-expanded") === "true";
  navToggle.setAttribute("aria-expanded", String(!expanded));
  topNav?.classList.toggle("is-open", !expanded);
});

const docsToggle = document.querySelector("[data-docs-toggle]");
docsToggle?.addEventListener("click", () => {
  const expanded = docsToggle.getAttribute("aria-expanded") === "true";
  docsToggle.setAttribute("aria-expanded", String(!expanded));
  document.body.classList.toggle("docs-nav-open", !expanded);
});

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  document.body.classList.remove("docs-nav-open");
  docsToggle?.setAttribute("aria-expanded", "false");
  topNav?.classList.remove("is-open");
  navToggle?.setAttribute("aria-expanded", "false");
});
