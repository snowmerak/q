export default {
  layout: "layouts/doc.njk",
  bodyClass: "docs-page",
  hideFooter: true,
  eleventyComputed: {
    permalink: (data) => `${data.page.filePathStem.replace("/docs", "")}/index.html`,
    rawUrl: (data) => `/md${data.page.filePathStem.replace("/docs", "")}.md`,
  },
};
