const state = { range: "24h", model: "", role: "", models: new Set(), roles: new Set() };
const colors = ["#5f8cff", "#42c8e9", "#a678f5", "#59d39b", "#edae61", "#e87a90", "#7fb36a"];
const byId = (id) => document.getElementById(id);
const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 2 });
const integer = new Intl.NumberFormat();

function rangeStart(range, now) {
  const durations = { "24h": 864e5, "7d": 7 * 864e5, "30d": 30 * 864e5, "90d": 90 * 864e5 };
  return range === "all" ? new Date("2000-01-01T00:00:00Z") : new Date(now.getTime() - durations[range]);
}

function percentage(part, whole) {
  return whole > 0 ? `${(part * 100 / whole).toFixed(part === whole ? 0 : 1)}%` : "0%";
}

function updateSelect(select, values, selected, allLabel) {
  const known = new Set(Array.from(select.options).map((option) => option.value));
  [...values].sort().forEach((value) => {
    if (!known.has(value)) select.add(new Option(value, value));
  });
  select.options[0].textContent = allLabel;
  select.value = selected;
}

async function loadUsage() {
  const status = byId("status");
  status.className = "status";
  status.textContent = "Refreshing local usage…";
  const now = new Date();
  const params = new URLSearchParams({ from: rangeStart(state.range, now).toISOString(), to: now.toISOString() });
  if (state.model) params.set("model", state.model);
  if (state.role) params.set("role", state.role);
  try {
    const response = await fetch(`/api/v1/usage?${params}`, { headers: { Accept: "application/json" }, cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const data = await response.json();
    data.models.forEach((item) => state.models.add(item.name));
    data.roles.forEach((item) => state.roles.add(item.name));
    updateSelect(byId("model-filter"), state.models, state.model, "All models");
    updateSelect(byId("role-filter"), state.roles, state.role, "All roles");
    render(data);
    status.textContent = `${data.resolution === "hour" ? "Hourly" : "Daily"} rollup · updated ${now.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
  } catch (error) {
    status.className = "status error";
    status.textContent = `Usage service unavailable: ${error.message}`;
  }
}

function render(data) {
  const totals = data.totals;
  byId("total-tokens").textContent = compact.format(totals.total_tokens);
  byId("calls").textContent = integer.format(totals.calls);
  byId("cache-rate").textContent = percentage(totals.cached_tokens, totals.prompt_tokens);
  byId("estimate-rate").textContent = percentage(totals.estimated_calls, totals.calls);
  byId("period-label").textContent = state.range === "all" ? "all recorded time" : `last ${state.range}`;
  byId("tokens-per-call").textContent = `${compact.format(totals.calls ? Math.round(totals.total_tokens / totals.calls) : 0)} per call`;
  byId("cached-tokens").textContent = `${compact.format(totals.cached_tokens)} cached tokens`;
  byId("estimated-calls").textContent = `${integer.format(totals.estimated_calls)} calls`;
  renderChart(data.series);
  renderRoles(data.roles);
  renderModels(data.models);
}

function renderChart(series) {
  const host = byId("chart");
  host.replaceChildren();
  if (!series.length) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = "No token events in this range";
    host.append(empty);
    return;
  }
  const width = 1000, height = 250, left = 54, bottom = 28, top = 8, plotWidth = width - left, plotHeight = height - bottom - top;
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.setAttribute("preserveAspectRatio", "none");
  const totals = series.map((point) => point.prompt_tokens + point.completion_tokens);
  const maximum = Math.max(...totals, 1);
  for (let line = 0; line <= 4; line++) {
    const y = top + plotHeight * line / 4;
    const grid = document.createElementNS(svg.namespaceURI, "line");
    grid.setAttribute("x1", left); grid.setAttribute("x2", width); grid.setAttribute("y1", y); grid.setAttribute("y2", y); grid.setAttribute("class", "grid");
    svg.append(grid);
    const label = document.createElementNS(svg.namespaceURI, "text");
    label.setAttribute("x", left - 8); label.setAttribute("y", y + 3); label.setAttribute("text-anchor", "end"); label.setAttribute("class", "axis");
    label.textContent = compact.format(maximum * (4 - line) / 4);
    svg.append(label);
  }
  const barWidth = Math.max(2, plotWidth / series.length - 2);
  series.forEach((point, index) => {
    const x = left + index * plotWidth / series.length + 1;
    const parts = [
      { value: Math.max(0, point.prompt_tokens - point.cached_tokens), color: "#5f8cff" },
      { value: point.cached_tokens, color: "#42c8e9" },
      { value: point.completion_tokens, color: "#a678f5" }
    ];
    let base = height - bottom;
    parts.forEach((part) => {
      const partHeight = plotHeight * part.value / maximum;
      base -= partHeight;
      const rect = document.createElementNS(svg.namespaceURI, "rect");
      rect.setAttribute("x", x); rect.setAttribute("y", base); rect.setAttribute("width", barWidth); rect.setAttribute("height", Math.max(0, partHeight)); rect.setAttribute("fill", part.color); rect.setAttribute("rx", "1");
      svg.append(rect);
    });
  });
  [0, Math.floor((series.length - 1) / 2), series.length - 1].filter((value, index, all) => all.indexOf(value) === index).forEach((index) => {
    const label = document.createElementNS(svg.namespaceURI, "text");
    label.setAttribute("x", left + (index + .5) * plotWidth / series.length); label.setAttribute("y", height - 5); label.setAttribute("text-anchor", "middle"); label.setAttribute("class", "axis");
    const date = new Date(series[index].bucket);
    label.textContent = Number.isNaN(date.getTime()) ? series[index].bucket.slice(5) : date.toLocaleString([], dataLabelOptions(series.length));
    svg.append(label);
  });
  host.append(svg);
}

function dataLabelOptions(points) {
  return points > 48 ? { month: "short", day: "numeric" } : { weekday: "short", hour: "2-digit" };
}

function renderRoles(roles) {
  const strip = byId("role-strip"), legend = byId("role-legend");
  strip.replaceChildren(); legend.replaceChildren();
  const total = roles.reduce((sum, role) => sum + role.total_tokens, 0);
  if (!roles.length) {
    const value = document.createElement("span"); value.style.flexBasis = "100%"; strip.append(value);
    const empty = document.createElement("span"); empty.textContent = "No role data"; legend.append(empty); return;
  }
  roles.forEach((role, index) => {
    const color = colors[index % colors.length];
    const segment = document.createElement("span"); segment.style.flexBasis = `${total ? role.total_tokens * 100 / total : 0}%`; segment.style.background = color; segment.title = `${role.name}: ${integer.format(role.total_tokens)}`; strip.append(segment);
    const item = document.createElement("span"), dot = document.createElement("i"), name = document.createElement("b");
    dot.style.background = color; name.textContent = role.name; item.append(dot, name, document.createTextNode(`${percentage(role.total_tokens, total)} · ${compact.format(role.total_tokens)}`)); legend.append(item);
  });
}

function renderModels(models) {
  const body = byId("model-rows"); body.replaceChildren();
  if (!models.length) {
    const row = document.createElement("tr"), cell = document.createElement("td"); row.className = "empty-row"; cell.colSpan = 7; cell.textContent = "No model calls in this range"; row.append(cell); body.append(row); return;
  }
  models.forEach((model) => {
    const values = [model.name, integer.format(model.calls), compact.format(model.prompt_tokens), compact.format(model.completion_tokens), compact.format(model.cached_tokens), compact.format(model.total_tokens), percentage(model.estimated_calls, model.calls)];
    const row = document.createElement("tr");
    values.forEach((value) => { const cell = document.createElement("td"); cell.textContent = value; row.append(cell); });
    body.append(row);
  });
}

document.querySelectorAll("[data-range]").forEach((button) => button.addEventListener("click", () => {
  state.range = button.dataset.range;
  document.querySelectorAll("[data-range]").forEach((candidate) => candidate.classList.toggle("active", candidate === button));
  loadUsage();
}));
byId("model-filter").addEventListener("change", (event) => { state.model = event.target.value; loadUsage(); });
byId("role-filter").addEventListener("change", (event) => { state.role = event.target.value; loadUsage(); });
loadUsage();
