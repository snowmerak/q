# JavaScript workflows

Use this mode for known multistep flows, condition waits, loops, and complete
structured extraction. Node.js 18+ is required only for `run`; no npm package
is needed. Keep global session/browser flags before `run` and `--node PATH`
before the script path. Source may be a file or `-` for stdin (up to 1 MiB).
Scripts are async function bodies, with top-level await/return and `require`,
not ES modules. Run only scripts authored/reviewed for the user's task: they
have the local user's Node filesystem/process/network access.

## Observe structure, wait for readiness, extract the requested set

First inspect the page and choose its actual selectors and readiness rule.
For a known loaded list in an existing tab, an adaptable `extract.js` is:

```js
const page = session.page("main");
const css = session.args[0];
const expected = Number(session.args[1]);
await page.waitForFunction((selector, count) =>
  document.querySelectorAll(selector).length >= count, css, expected);
return await page.evaluate(selector => {
  const links = [...document.querySelectorAll(selector)].map(a => ({
    title: a.textContent.trim(), url: a.href,
  }));
  return { count: links.length, links };
}, css);
```

```sh
cdp-q-driver --session research --timeout 60s run extract.js "#list a" 5
```

Replace the selector and expected count with observed values. The count is a
readiness lower bound, not an extraction cap. Collect every matching item,
report missing links, and handle pagination/lazy loading when the user asks
for all results. Do not claim loaded DOM items are the site's complete set.

## API essentials

- `session.page(name)` returns a local handle to an existing named tab;
  `await session.newPage(name, url, {task:"default"})` creates a managed tab.
  `await session.pages()` lists managed tabs. First browser use starts an
  absent session with the CLI's normal discovery/saved settings.
- `page.goto(url)`, `title()`, `url()`, `frames()`, `snapshot({scope:"full_page"})`,
  `activate()`, `close()`, and `release()` keep the CLI's current meanings.
- `page.click(locator)`, `fill(locator,text)`, `press(locator,key)`, and
  `upload(locator,absolutePaths)` use existing native input/validation. A locator
  is CSS text or `{css|ref|text|role, name?, frame?}` with exactly one selector.
  Example: `await page.fill({role:"textbox",name:"Search"}, "한글 검색")`.
- `page.evaluate(expressionOrFunction,...jsonArgs)` awaits a returned page
  Promise. `page.waitForFunction(expressionOrFunction,...jsonArgs)` polls a
  synchronous truthy condition. The callbacks run in the page, without host
  closures, `session`, or `require`; pass values through JSON arguments.
  Both invalidate refs: take a new snapshot before using refs afterward.
- `page.waitForSelector(locator)` waits for one unique visible element;
  multiple matches fail. `page.waitForIdle("500ms")` observes request quiet,
  which does not prove list readiness. `page.screenshot(path)` creates a PNG.
- `await session.command(...strings)` accepts existing CLI command tokens and
  returns the unwrapped result. Use it for tasks/events/dialogs/files, e.g.
  `const {subscription} = await session.command("events","arm","--kind","popup",page.name)`.
  Then trigger once, `events next`, handle/adopt, and close the subscription
  in `finally`. Calls execute sequentially: do not queue an event wait before
  its trigger. Session lifecycle commands and nested run are excluded.

## Outcomes and cleanup

Await every browser call. `return` a JSON-serializable value (undefined becomes
null); console logs go to stderr and stdout is reserved. Driver errors expose
`code`, `mayHaveEffects`, and partial `result` (including action receipts).
Inspect and reconcile errors; do not replay input whose outcome is uncertain.
Uncaught script exceptions use `script_error`; earlier actions are not rolled
back. An error may report effects from earlier workflow steps too.

The global `--timeout` covers the entire execution; each operation also has
the saved SDK command timeout. On timeout the Node host is terminated and the
active RPC is canceled. Page-side JavaScript may still continue, and actions
already applied remain applied. Running/failing a script leaves tabs/tasks and
the session alive. Apply the skill's normal ownership-based cleanup rules.
For lifecycle changes use the CLI outside the script.
