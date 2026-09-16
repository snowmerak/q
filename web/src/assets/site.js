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
  const copied = document.execCommand("copy");
  input.remove();
  if (!copied) throw new Error("Clipboard copy failed");
};

const locale = document.body.dataset.locale || "en";
const messages = ({
  en: { copy: "Copy", copied: "Copied", copiedMd: "Copied MD", copyFailed: "Copy failed", markdownCopied: "Markdown copied.", markdownFailed: "Markdown could not be copied.", codeCopied: "Code copied.", commandCopied: "Command copied." },
  ko: { copy: "복사", copied: "복사됨", copiedMd: "Markdown 복사됨", copyFailed: "복사 실패", markdownCopied: "Markdown을 복사했습니다.", markdownFailed: "Markdown을 복사하지 못했습니다.", codeCopied: "코드를 복사했습니다.", commandCopied: "명령어를 복사했습니다." },
  ja: { copy: "コピー", copied: "コピー済み", copiedMd: "Markdown をコピー済み", copyFailed: "コピー失敗", markdownCopied: "Markdown をコピーしました。", markdownFailed: "Markdown をコピーできませんでした。", codeCopied: "コードをコピーしました。", commandCopied: "コマンドをコピーしました。" },
  "zh-cn": { copy: "复制", copied: "已复制", copiedMd: "已复制 Markdown", copyFailed: "复制失败", markdownCopied: "已复制 Markdown。", markdownFailed: "无法复制 Markdown。", codeCopied: "已复制代码。", commandCopied: "已复制命令。" },
})[locale] || null;
const text = messages || { copy: "Copy", copied: "Copied", copiedMd: "Copied MD", copyFailed: "Copy failed", markdownCopied: "Markdown copied.", markdownFailed: "Markdown could not be copied.", codeCopied: "Code copied.", commandCopied: "Command copied." };

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
  window.setTimeout(() => { label.textContent = button.dataset.originalLabel || text.copy; }, 1800);
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
      setTemporaryLabel(button, label, text.copiedMd);
      showToast(text.markdownCopied);
    } catch {
      setTemporaryLabel(button, label, text.copyFailed);
      showToast(text.markdownFailed);
    }
  });
});

document.querySelectorAll("[data-copy-code]").forEach((button) => {
  const label = button.querySelector("span");
  if (label) label.textContent = text.copy;
  button.setAttribute("aria-label", ({ en: "Copy code", ko: "코드 복사", ja: "コードをコピー", "zh-cn": "复制代码" })[locale] || "Copy code");
  button.dataset.originalLabel = label?.textContent || text.copy;
  button.addEventListener("click", async () => {
    const code = button.closest("[data-code-block]")?.querySelector("code")?.textContent;
    if (!code || !label) return;
    await copyText(code);
    setTemporaryLabel(button, label, text.copied);
    showToast(text.codeCopied);
  });
});

document.querySelectorAll("[data-copy-value]").forEach((button) => {
  const label = button.querySelector("span");
  button.dataset.originalLabel = label?.textContent || text.copy;
  button.addEventListener("click", async () => {
    if (!label) return;
    await copyText(button.dataset.copyValue || "");
    setTemporaryLabel(button, label, text.copied);
    showToast(text.commandCopied);
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
