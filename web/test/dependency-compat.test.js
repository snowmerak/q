import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

const require = createRequire(import.meta.url);
const chokidarRequire = createRequire(require.resolve("chokidar"));
const braces = chokidarRequire("braces");

test("Chokidar brace expansion preserves glob alternatives and bounds nesting", () => {
  assert.deepEqual(braces.expand("src/{ko,ja}/**/*.{md,njk}"), [
    "src/ko/**/*.md", "src/ko/**/*.njk",
    "src/ja/**/*.md", "src/ja/**/*.njk",
  ]);
  assert.deepEqual(braces.expand("page-{01..03}.md"), [
    "page-01.md", "page-02.md", "page-03.md",
  ]);
  const deeplyNested = "{".repeat(6000) + "a,b" + "}".repeat(6000);
  assert.doesNotThrow(() => braces.expand(deeplyNested));
});

async function waitForContent(path, expected) {
  const deadline = Date.now() + 10_000;
  let content = "";
  while (Date.now() < deadline) {
    try {
      content = await readFile(path, "utf8");
      if (content.includes(expected)) return;
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
    await delay(50);
  }
  assert.fail(`Timed out waiting for ${path} to contain ${expected}: ${content}`);
}

test("Eleventy renders YAML and rebuilds watched pages, includes and data", { timeout: 45_000 }, async (t) => {
  const originalCwd = process.cwd();
  const fixture = await mkdtemp(join(tmpdir(), "q-docs-dependencies-"));
  let eleventy;
  t.after(async () => {
    try {
      await eleventy?.stopWatch();
    } finally {
      process.chdir(originalCwd);
      await rm(fixture, { recursive: true, force: true });
    }
  });
  await mkdir(join(fixture, "src/_includes"), { recursive: true });
  await mkdir(join(fixture, "src/_data"), { recursive: true });
  const layoutPath = join(fixture, "src/_includes/base.njk");
  const dataPath = join(fixture, "src/_data/site.json");
  const pagePath = join(fixture, "src/page.md");
  const page = (title) => `---
layout: base.njk
permalink: /example/
title: ${title}
labels: [one, two]
enabled: true
published: 2026-10-10
---
{{ title }} / {{ labels | join(',') }} / {{ enabled }} / {{ published.toISOString() }} / {{ site.label }}
`;
  await writeFile(layoutPath, "layout-first {{ content | safe }}");
  await writeFile(dataPath, JSON.stringify({ label: "data-first" }));
  await writeFile(pagePath, page("page-first"));
  const configPath = join(fixture, "eleventy.config.cjs");
  await writeFile(configPath, "module.exports = () => ({ markdownTemplateEngine: 'njk', templateFormats: ['md', 'njk'] });");
  process.chdir(fixture);
  const { default: Eleventy } = await import("@11ty/eleventy");
  eleventy = new Eleventy("src", "_site", {
    configPath,
    quietMode: true,
    runMode: "watch",
  });
  await eleventy.init();
  await eleventy.watch();
  const outputPath = join(fixture, "_site/example/index.html");
  await waitForContent(outputPath, "page-first / one,two / true / 2026-10-10T00:00:00.000Z / data-first");

  await writeFile(pagePath, page("page-updated"));
  await waitForContent(outputPath, "page-updated");
  await writeFile(layoutPath, "layout-updated {{ content | safe }}");
  await waitForContent(outputPath, "layout-updated");
  await writeFile(dataPath, JSON.stringify({ label: "data-updated" }));
  await waitForContent(outputPath, "data-updated");

  await mkdir(join(fixture, "src/new/nested"), { recursive: true });
  const added = once(eleventy.watcher, "add", { signal: AbortSignal.timeout(10_000) });
  await writeFile(join(fixture, "src/new/nested/page.md"), "---\npermalink: /added/\n---\nnew-page");
  const [addedPath] = await added;
  assert.equal(addedPath.replaceAll("\\", "/"), "src/new/nested/page.md");
  if (process.platform === "win32") {
    // Eleventy 3's search cache does not normalize newly added Windows paths.
    // This also reproduces without the overrides; verify detection and a fresh
    // build here rather than treating that upstream bug as a dependency regression.
    await eleventy.stopWatch();
    const freshBuild = new Eleventy("src", "_site", { configPath, quietMode: true });
    await freshBuild.write();
  }
  await waitForContent(join(fixture, "_site/added/index.html"), "new-page");
});
