# q documentation site

The public guide for q is an Eleventy static site styled with
`merak-protocol-design-system`.

```powershell
npm install
npm run dev
npm run build
npm test
```

Cloudflare Pages settings:

- Root directory: `web`
- Build command: `npm run build`
- Build output directory: `_site`
- Custom domain: `q.saturday.ne.kr`

Documentation lives in `src/docs`. Each page is rendered as HTML and copied to
the corresponding `/md/` URL so readers and agents can retrieve the source.

Dependency overrides keep Eleventy 3's build and watch behavior while removing
two dependencies with unpatched denial-of-service advisories:

- Chokidar 3 only calls `braces.expand`. Its `braces` dependency is replaced
  with `brace-expansion` 5, which exposes that API and bounds nesting and output.
  This override is scoped to Chokidar; it is not a general replacement for the
  full `braces` API.
- `js-yaml` is unified on 4.x, removing the old `argparse` / `sprintf-js` chain.
  Eleventy 3 already supplies `yaml.load` as gray-matter's YAML engine, so it
  does not call the removed `safeLoad` API.

`npm test` checks the replacement's depth limit and Eleventy's YAML rendering,
glob watching, template/include/data changes, and detection of new nested pages.
On Windows, Eleventy 3 has an existing path normalization bug that prevents new
pages from entering its watch cache. The test checks their detection and a fresh
build; restart the development server after adding pages on Windows.
Revisit these overrides when upgrading Eleventy, and run `npm audit` alongside
the tests and build.
