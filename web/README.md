# q documentation site

The public guide for q is an Eleventy static site styled with
`merak-protocol-design-system`.

```powershell
npm install
npm run dev
npm run build
```

Cloudflare Pages settings:

- Root directory: `web`
- Build command: `npm run build`
- Build output directory: `_site`
- Custom domain: `q.saturday.ne.kr`

Documentation lives in `src/docs`. Each page is rendered as HTML and copied to
the corresponding `/md/` URL so readers and agents can retrieve the source.
