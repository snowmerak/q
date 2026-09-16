import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

const versioned = (name) => {
  const content = readFileSync(new URL(`../assets/${name}`, import.meta.url));
  const hash = createHash("sha256").update(content).digest("hex").slice(0, 12);
  return `/assets/${name}?v=${hash}`;
};

export default {
  siteCss: versioned("site.css"),
  siteJs: versioned("site.js"),
};
