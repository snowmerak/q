import markdownIt from "markdown-it";
import markdownItAnchor from "markdown-it-anchor";
import { navigationFor } from "./src/_data/navigation.js";

const localeIds = ["ko", "ja", "zh-cn"];
const stripLocale = (value = "/") => {
  const normalized = value.startsWith("/") ? value : `/${value}`;
  const match = normalized.match(/^\/(ko|ja|zh-cn)(?=\/|$)/);
  if (!match) return normalized;
  const stripped = normalized.slice(match[0].length);
  return stripped === "" ? "/" : stripped;
};

const localeUrl = (value = "/", locale = "en") => {
  const path = stripLocale(value);
  if (!localeIds.includes(locale)) return path;
  return path === "/" ? `/${locale}/` : `/${locale}${path}`;
};

const escapeAttribute = (value) =>
  String(value).replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[character]);

export default function (eleventyConfig) {
  const markdown = markdownIt({
    html: false,
    linkify: true,
    typographer: true,
  }).use(markdownItAnchor, {
    slugify: (value) => value
      .toLowerCase()
      .trim()
      .replace(/[^\p{L}\p{N}\s-]/gu, "")
      .replace(/\s+/g, "-"),
  });

  const defaultFence = markdown.renderer.rules.fence.bind(markdown.renderer.rules);
  markdown.renderer.rules.fence = (tokens, index, options, env, self) => {
    const token = tokens[index];
    const language = token.info.trim().split(/\s+/)[0] || "text";
    const code = markdown.utils.escapeHtml(token.content.replace(/\n$/, ""));
    const label = escapeAttribute(language);
    return `<div class="mp-command-result doc-code" data-code-block>
      <div class="doc-code__bar"><span class="mp-command-result__label">${label}</span><button class="code-copy" type="button" data-copy-code aria-label="Copy code"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="8" y="8" width="11" height="11" rx="2"></rect><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"></path></svg><span>Copy</span></button></div>
      <code class="mp-command-result__value">${code}</code>
    </div>`;
  };

  markdown.renderer.rules.bullet_list_open = () => '<ul class="mp-list">';
  markdown.renderer.rules.ordered_list_open = () => '<ol class="mp-list">';
  markdown.renderer.rules.table_open = () => '<div class="table-shell"><table class="mp-table">';
  markdown.renderer.rules.table_close = () => '</table></div>';
  markdown.renderer.rules.blockquote_open = () => '<aside class="mp-alert mp-alert--info" role="note"><div class="mp-alert__content">';
  markdown.renderer.rules.blockquote_close = () => '</div></aside>';
  markdown.renderer.rules.fence.default = defaultFence;

  eleventyConfig.setLibrary("md", markdown);
  eleventyConfig.addPassthroughCopy({ "src/assets": "assets" });
  eleventyConfig.addPassthroughCopy({ "src/public": "." });
  eleventyConfig.addPassthroughCopy({
    "node_modules/merak-protocol-design-system/src/style.css": "assets/merak/style.css",
  });
  eleventyConfig.addPassthroughCopy({
    "node_modules/merak-protocol-design-system/src/styles": "assets/merak/styles",
  });
  eleventyConfig.addPassthroughCopy({ "src/docs/guide": "md/guide" });
  eleventyConfig.addPassthroughCopy({ "src/docs/workflows": "md/workflows" });
  eleventyConfig.addPassthroughCopy({ "src/docs/concepts": "md/concepts" });
  eleventyConfig.addPassthroughCopy({ "src/docs/reference": "md/reference" });
  eleventyConfig.addPassthroughCopy({ "src/docs/ko": "md/ko" });
  eleventyConfig.addPassthroughCopy({ "src/docs/ja": "md/ja" });
  eleventyConfig.addPassthroughCopy({ "src/docs/zh-cn": "md/zh-cn" });

  eleventyConfig.addFilter("startsWith", (value = "", prefix = "") => value.startsWith(prefix));
  eleventyConfig.addFilter("stripLocale", stripLocale);
  eleventyConfig.addFilter("localeUrl", localeUrl);
  eleventyConfig.addFilter("htmlLang", (locale = "en") => locale === "zh-cn" ? "zh-CN" : locale);
  eleventyConfig.addFilter("localeShort", (locale = "en") => ({ en: "EN", ko: "KO", ja: "JA", "zh-cn": "简中" })[locale] || "EN");
  eleventyConfig.addFilter("localizedNavigation", (locale = "en") => navigationFor(locale));
  eleventyConfig.addFilter("navNeighbors", (url, locale = "en") => {
    const items = navigationFor(locale).flatMap((group) => group.items);
    const current = items.findIndex((item) => item.url === url);
    return {
      previous: current > 0 ? items[current - 1] : null,
      next: current >= 0 && current < items.length - 1 ? items[current + 1] : null,
    };
  });

  return {
    dir: {
      input: "src",
      includes: "_includes",
      data: "_data",
      output: "_site",
    },
    markdownTemplateEngine: "njk",
    htmlTemplateEngine: "njk",
    templateFormats: ["md", "njk"],
  };
}
