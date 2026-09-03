// codewalk web application.
//
// The UI consumes exactly the same canonical walkthrough as the CLI and the
// JSON output; it adds presentation, not meaning. Two rules shape it:
//
//   1. Progressive disclosure. One step at a time, deep dives closed, evidence
//      available but never in the reader's way.
//   2. Model output is untrusted text. It is escaped before rendering, and the
//      small Markdown subset below is applied to already-escaped content, so a
//      walkthrough can never inject markup.

const api = {
  async get(path) {
    const res = await fetch(path);
    if (!res.ok) throw new Error((await safeError(res)) || res.statusText);
    return res.json();
  },
  async post(path, body) {
    const res = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body ?? {}),
    });
    if (!res.ok) throw new Error((await safeError(res)) || res.statusText);
    return res.json();
  },
};

async function safeError(res) {
  try {
    const body = await res.json();
    return body.error;
  } catch {
    return null;
  }
}

const state = {
  runs: [],
  runId: null,
  run: null,
  walkthrough: null,
  tab: "walkthrough",
  stepIndex: 0,
  eventSource: null,
};

const el = (id) => document.getElementById(id);

/* ------------------------------------------------------------------ escaping */

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

// renderMarkdown supports the subset walkthrough prose actually uses:
// paragraphs, lists, fenced code, inline code, bold, italics and links. It runs
// on escaped text, so nothing in a walkthrough can produce live markup.
function renderMarkdown(source) {
  const text = escapeHTML(source ?? "").replace(/\r\n/g, "\n");
  const blocks = [];
  let list = null;
  let inCode = false;
  let code = [];

  const flushList = () => {
    if (list) {
      blocks.push(`<${list.tag}>${list.items.map((i) => `<li>${inline(i)}</li>`).join("")}</${list.tag}>`);
      list = null;
    }
  };

  for (const line of text.split("\n")) {
    if (line.trimStart().startsWith("```")) {
      if (inCode) {
        blocks.push(`<pre><code>${code.join("\n")}</code></pre>`);
        code = [];
        inCode = false;
      } else {
        flushList();
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      code.push(line);
      continue;
    }
    const bullet = line.match(/^\s*[-*]\s+(.*)$/);
    const numbered = line.match(/^\s*\d+\.\s+(.*)$/);
    if (bullet) {
      if (!list || list.tag !== "ul") { flushList(); list = { tag: "ul", items: [] }; }
      list.items.push(bullet[1]);
      continue;
    }
    if (numbered) {
      if (!list || list.tag !== "ol") { flushList(); list = { tag: "ol", items: [] }; }
      list.items.push(numbered[1]);
      continue;
    }
    if (line.trim() === "") { flushList(); continue; }
    flushList();
    blocks.push(`<p>${inline(line)}</p>`);
  }
  flushList();
  if (inCode && code.length) blocks.push(`<pre><code>${code.join("\n")}</code></pre>`);
  return blocks.join("");
}

function inline(text) {
  return text
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[\s(])\*([^*\n]+)\*/g, "$1<em>$2</em>")
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer noopener">$1</a>');
}

/* ------------------------------------------------------------------ diagrams */

let mermaidReady = false;
function initMermaid() {
  if (mermaidReady || typeof window.mermaid === "undefined") return;
  const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  window.mermaid.initialize({
    startOnLoad: false,
    theme: dark ? "dark" : "neutral",
    securityLevel: "strict",
    fontFamily: "ui-sans-serif, system-ui, sans-serif",
  });
  mermaidReady = true;
}

async function renderDiagrams(root) {
  initMermaid();
  if (!mermaidReady) return;
  const nodes = root.querySelectorAll("[data-mermaid]");
  for (const node of nodes) {
    const source = node.getAttribute("data-mermaid");
    try {
      const { svg } = await window.mermaid.render(`d${Math.random().toString(36).slice(2)}`, source);
      node.innerHTML = svg;
    } catch (err) {
      // A diagram that does not render must not hide its content: show the
      // source so the reader still gets the information.
      node.innerHTML = `<pre>${escapeHTML(source)}</pre>
        <p class="error">This diagram could not be rendered.</p>`;
    }
  }
}

/* ------------------------------------------------------------------ sidebar */

async function loadRepository() {
  const summary = el("repo-summary");
  try {
    const repo = await api.get("/api/v1/repository");
    const parts = [`<strong>${escapeHTML(repo.name)}</strong>`];
    if (repo.branch) parts.push(`on <strong>${escapeHTML(repo.branch)}</strong>`);
    if (repo.default_branch) parts.push(`base <strong>${escapeHTML(repo.default_branch)}</strong>`);
    if (repo.has_changes) parts.push("· uncommitted changes");
    summary.innerHTML = parts.join(" ");
  } catch (err) {
    summary.textContent = `No repository detected: ${err.message}`;
  }
}

async function loadRuns() {
  try {
    const data = await api.get("/api/v1/walkthroughs");
    state.runs = data.walkthroughs || [];
  } catch {
    state.runs = [];
  }
  const list = el("run-list");
  list.innerHTML = "";
  if (!state.runs.length) {
    list.innerHTML = `<li class="run-meta">No walkthroughs yet.</li>`;
    return;
  }
  for (const run of state.runs) {
    const li = document.createElement("li");
    li.className = run.id === state.runId ? "active" : "";
    li.innerHTML = `<span class="run-title">${escapeHTML(run.title || run.scope || run.id)}</span>
      <span class="run-meta">${escapeHTML(run.repository)} · ${escapeHTML(run.scope || "")} · ${relativeTime(run.created_at)}</span>`;
    li.addEventListener("click", () => openWalkthrough(run.id));
    list.appendChild(li);
  }
}

function relativeTime(iso) {
  const then = new Date(iso).getTime();
  if (!then) return "";
  const mins = Math.round((Date.now() - then) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

/* ------------------------------------------------------------------ generate */

async function generate() {
  const button = el("generate");
  const progress = el("progress");
  button.disabled = true;
  progress.classList.remove("hidden");
  progress.innerHTML = "";

  const type = el("wt-type").value;
  const body = {
    type,
    selector: el("wt-selector").value,
    base: el("wt-base").value.trim(),
    range: el("wt-range").value.trim(),
    focus: el("wt-focus").value.trim(),
  };
  try {
    const job = await api.post("/api/v1/walkthroughs", body);
    watchJob(job.job_id);
  } catch (err) {
    progress.innerHTML = `<div class="error">${escapeHTML(err.message)}</div>`;
    button.disabled = false;
  }
}

function watchJob(jobId) {
  const progress = el("progress");
  if (state.eventSource) state.eventSource.close();
  const source = new EventSource(`/api/v1/jobs/${jobId}/events`);
  state.eventSource = source;

  source.addEventListener("progress", (event) => {
    const e = JSON.parse(event.data);
    const line = document.createElement("div");
    if (e.kind === "stage_start") {
      line.className = "stage";
      line.textContent = `▸ ${stageLabel(e.role)}`;
    } else if (e.kind === "tool_call") {
      line.textContent = `   ${e.tool} ${truncate(e.detail || "", 48)}`;
    } else if (e.kind === "note") {
      line.textContent = `   ${e.detail}`;
    } else {
      return;
    }
    progress.appendChild(line);
    progress.scrollTop = progress.scrollHeight;
  });

  source.addEventListener("done", async (event) => {
    const status = JSON.parse(event.data);
    source.close();
    el("generate").disabled = false;
    if (status.status === "complete" && status.run_id) {
      progress.classList.add("hidden");
      await loadRuns();
      await openWalkthrough(status.run_id);
    } else {
      progress.innerHTML += `<div class="error">${escapeHTML(status.error || "generation failed")}</div>`;
    }
  });

  source.onerror = () => {
    source.close();
    el("generate").disabled = false;
  };
}

const STAGE_LABELS = {
  investigator: "Investigating the repository",
  mental_model: "Working out what matters",
  planner: "Planning the walkthrough",
  author: "Writing the walkthrough",
  editor: "Editing for clarity",
  grounding: "Checking against the code",
  correction: "Applying corrections",
  followup: "Answering",
};

const stageLabel = (role) => STAGE_LABELS[role] || role;
const truncate = (s, n) => (s.length > n ? `${s.slice(0, n)}…` : s);

/* ------------------------------------------------------------------ rendering */

async function openWalkthrough(runId) {
  const data = await api.get(`/api/v1/walkthroughs/${runId}`);
  state.runId = runId;
  state.run = data.run;
  state.walkthrough = data.walkthrough;
  state.stepIndex = 0;
  state.tab = "walkthrough";
  el("empty-state").classList.add("hidden");
  el("walkthrough").classList.remove("hidden");
  el("chat-toggle").classList.remove("hidden");
  await loadRuns();
  await loadConversation();
  render();
}

function render() {
  const w = state.walkthrough;
  if (!w) return;
  renderHeader(w);
  renderTabs(w);
  renderTab(w);
}

function renderHeader(w) {
  const scope = w.scope || {};
  const chips = [];
  chips.push(`<span class="chip level">level ${w.complexity?.level ?? "?"} · ${escapeHTML(w.complexity?.label || "")}</span>`);
  chips.push(`<span class="chip">${escapeHTML(scope.repository_name || "")}</span>`);
  if (w.kind === "change") {
    chips.push(`<span class="chip">${escapeHTML(scopeLabel(scope))}</span>`);
    chips.push(`<span class="chip">${scope.stats?.files_changed ?? 0} files · +${scope.stats?.insertions ?? 0}/−${scope.stats?.deletions ?? 0}</span>`);
  } else {
    chips.push(`<span class="chip">whole repository</span>`);
  }
  chips.push(`<span class="chip">${w.steps?.length ?? 0} steps</span>`);

  el("wt-header").innerHTML = `
    <h2>${escapeHTML(w.title)}</h2>
    <p class="headline">${escapeHTML(w.headline)}</p>
    ${w.summary ? `<p class="summary">${escapeHTML(w.summary)}</p>` : ""}
    <div class="chips">${chips.join("")}</div>`;
}

function scopeLabel(scope) {
  switch (scope.selector) {
    case "working-tree": return "uncommitted changes";
    case "staged": return "staged changes";
    case "commit": return `commit ${(scope.head_commit || "").slice(0, 8)}`;
    case "repository": return "whole repository";
    default: return `${scope.base || ""}..${scope.head || ""}`;
  }
}

function availableTabs(w) {
  const tabs = [["walkthrough", "Walkthrough"]];
  if (w.before_after?.aspects?.length) tabs.push(["before-after", "Before / after"]);
  if (w.architecture || w.components?.length) tabs.push(["architecture", "Architecture"]);
  if (w.concepts?.length || w.glossary?.length) tabs.push(["concepts", "Concepts"]);
  if (w.start_here?.length || w.ignorable?.length) tabs.push(["reading", "What to read"]);
  if (w.uncertainties?.length) tabs.push(["open", "Not established"]);
  tabs.push(["evidence", "Evidence"]);
  return tabs;
}

function renderTabs(w) {
  const tabs = availableTabs(w);
  el("tabs").innerHTML = tabs
    .map(([id, label]) => `<button data-tab="${id}" class="${state.tab === id ? "active" : ""}">${label}</button>`)
    .join("");
  for (const button of el("tabs").querySelectorAll("button")) {
    button.addEventListener("click", () => {
      state.tab = button.dataset.tab;
      render();
    });
  }
}

function renderTab(w) {
  const container = el("tab-content");
  switch (state.tab) {
    case "before-after": container.innerHTML = renderBeforeAfter(w); break;
    case "architecture": container.innerHTML = renderArchitecture(w); break;
    case "concepts": container.innerHTML = renderConcepts(w); break;
    case "reading": container.innerHTML = renderReading(w); break;
    case "open": container.innerHTML = renderUncertainties(w); break;
    case "evidence": container.innerHTML = renderEvidence(w); break;
    default: renderStep(w, container); return;
  }
  wireRefs(container);
  renderDiagrams(container);
}

function renderStep(w, container) {
  const steps = w.steps || [];
  if (!steps.length) {
    container.innerHTML = "<p>This walkthrough has no steps.</p>";
    return;
  }
  const index = Math.min(state.stepIndex, steps.length - 1);
  const step = steps[index];

  const dots = steps
    .map((s, i) => `<button class="step-dot ${i === index ? "current" : i < index ? "done" : ""}"
      data-step="${i}" title="${escapeHTML(s.title)}"></button>`)
    .join("");

  const components = (step.components || [])
    .map((id) => (w.components || []).find((c) => c.id === id))
    .filter(Boolean)
    .map((c) => `<span class="chip">${escapeHTML(c.name)}</span>`)
    .join("");

  container.innerHTML = `
    <div class="step-progress">${dots}</div>
    <section class="step">
      <span class="step-index">Step ${index + 1} of ${steps.length}${step.kind ? ` · ${escapeHTML(step.kind)}` : ""}</span>
      <h3>${escapeHTML(step.title)}</h3>
      ${step.summary ? `<p class="step-summary">${escapeHTML(step.summary)}</p>` : ""}
      ${components ? `<div class="chips">${components}</div>` : ""}
      <div class="prose">${renderMarkdown(step.explanation)}</div>
      ${renderRefs(step.code_refs)}
      ${(step.diagrams || []).map(renderDiagram).join("")}
      ${renderDeepDive(step.deep_dive)}
    </section>
    <div class="step-nav">
      <button ${index === 0 ? "disabled" : ""} id="prev-step">← Previous</button>
      ${
        index < steps.length - 1
          ? `<span class="next-hint">Next: ${escapeHTML(steps[index + 1].title)}</span>
             <button id="next-step" class="primary" style="width:auto">Next →</button>`
          : `<span class="next-hint">End of the walkthrough</span>`
      }
    </div>
    ${index === steps.length - 1 ? renderFeedback() : ""}`;

  container.querySelectorAll(".step-dot").forEach((dot) =>
    dot.addEventListener("click", () => {
      state.stepIndex = Number(dot.dataset.step);
      render();
    })
  );
  el("prev-step")?.addEventListener("click", () => {
    state.stepIndex = Math.max(0, index - 1);
    render();
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
  el("next-step")?.addEventListener("click", () => {
    state.stepIndex = Math.min(steps.length - 1, index + 1);
    render();
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
  wireFeedback(container);
  wireRefs(container);
  renderDiagrams(container);
}

function renderRefs(refs) {
  if (!refs?.length) return "";
  return `<div class="refs">${refs
    .map(
      (ref) => `<button class="ref" data-path="${escapeHTML(ref.path)}" data-line="${ref.start_line || 0}"
          data-end="${ref.end_line || 0}" data-side="${escapeHTML(ref.side || "")}">
        <span class="path">${escapeHTML(refLabel(ref))}</span>
        ${ref.symbol ? `<span class="note">${escapeHTML(ref.symbol)}</span>` : ""}
        ${ref.note ? `<span class="note">— ${escapeHTML(ref.note)}</span>` : ""}
        ${ref.side === "before" ? `<span class="side">before</span>` : ""}
      </button>`
    )
    .join("")}</div>`;
}

function refLabel(ref) {
  let label = ref.path;
  if (ref.start_line) {
    label += `:${ref.start_line}`;
    if (ref.end_line && ref.end_line > ref.start_line) label += `-${ref.end_line}`;
  }
  return label;
}

function renderDiagram(diagram) {
  if (!diagram?.source) return "";
  return `<figure class="diagram">
    ${diagram.title ? `<h4>${escapeHTML(diagram.title)}</h4>` : ""}
    <div data-mermaid="${escapeHTML(diagram.source)}"></div>
    ${diagram.caption ? `<figcaption>${escapeHTML(diagram.caption)}</figcaption>` : ""}
  </figure>`;
}

function renderDeepDive(deepDive) {
  if (!deepDive?.explanation) return "";
  return `<details class="deep-dive">
    <summary>${escapeHTML(deepDive.title || "Deep dive")}</summary>
    <div class="prose">${renderMarkdown(deepDive.explanation)}</div>
    ${renderRefs(deepDive.code_refs)}
    ${(deepDive.diagrams || []).map(renderDiagram).join("")}
  </details>`;
}

function renderBeforeAfter(w) {
  const ba = w.before_after;
  const rows = (ba.aspects || [])
    .map(
      (a) => `<div class="ba-row">
        <h4>${escapeHTML(a.aspect)}</h4>
        <div class="ba-cells">
          <div class="ba-cell before"><span class="label">Before</span>${escapeHTML(a.before)}</div>
          <div class="ba-cell after"><span class="label">After</span>${escapeHTML(a.after)}</div>
        </div>
        ${a.significance ? `<div class="ba-significance">${escapeHTML(a.significance)}</div>` : ""}
      </div>`
    )
    .join("");
  return `${ba.summary ? `<div class="prose">${renderMarkdown(ba.summary)}</div>` : ""}<div class="ba-grid">${rows}</div>`;
}

function renderArchitecture(w) {
  let html = "";
  if (w.architecture?.overview) html += `<div class="prose">${renderMarkdown(w.architecture.overview)}</div>`;
  const archDiagram = (w.diagrams || []).find((d) => d.id === w.architecture?.diagram_id);
  if (archDiagram) html += renderDiagram(archDiagram);

  for (const group of w.architecture?.groups || []) {
    html += `<h3>${escapeHTML(group.name)}</h3>`;
    if (group.description) html += `<p>${escapeHTML(group.description)}</p>`;
    html += (group.components || [])
      .map((id) => (w.components || []).find((c) => c.id === id))
      .filter(Boolean)
      .map(componentCard)
      .join("");
  }
  const grouped = new Set((w.architecture?.groups || []).flatMap((g) => g.components || []));
  const ungrouped = (w.components || []).filter((c) => !grouped.has(c.id));
  if (ungrouped.length) {
    if (grouped.size) html += `<h3>Other components</h3>`;
    html += ungrouped.map(componentCard).join("");
  }
  const unused = (w.diagrams || []).filter((d) => d.id !== w.architecture?.diagram_id);
  html += unused.map(renderDiagram).join("");
  return html || "<p>No architecture information in this walkthrough.</p>";
}

function componentCard(c) {
  return `<div class="card">
    <h4>${escapeHTML(c.name)} ${c.status ? `<span class="status-${escapeHTML(c.status)}">${escapeHTML(c.status)}</span>` : ""}</h4>
    ${c.kind ? `<span class="kind">${escapeHTML(c.kind)}</span>` : ""}
    <p>${escapeHTML(c.responsibility || "")}</p>
    ${c.files?.length ? `<div class="files">${c.files.map(escapeHTML).join("<br>")}</div>` : ""}
  </div>`;
}

function renderConcepts(w) {
  let html = (w.concepts || [])
    .map(
      (c) => `<div class="card">
        <h4>${escapeHTML(c.name)}${c.preexisting ? ` <span class="kind">existing</span>` : ` <span class="kind status-new">new</span>`}</h4>
        <p>${escapeHTML(c.summary)}</p>
        ${c.why_it_matters ? `<p class="kind">${escapeHTML(c.why_it_matters)}</p>` : ""}
      </div>`
    )
    .join("");
  if (w.glossary?.length) {
    html += `<h3>Glossary</h3>`;
    html += w.glossary
      .map(
        (g) => `<div class="card"><h4>${escapeHTML(g.term)}</h4><p>${escapeHTML(g.definition)}</p>
          ${g.used_here?.length ? `<div class="files">${g.used_here.map(escapeHTML).join("<br>")}</div>` : ""}</div>`
      )
      .join("");
  }
  return html || "<p>No concepts recorded.</p>";
}

function renderReading(w) {
  let html = "";
  if (w.start_here?.length) {
    html += `<h3>Start here</h3>${renderRefs(w.start_here)}`;
  }
  if (w.ignorable?.length) {
    html += `<h3>Safe to skip for now</h3>`;
    html += w.ignorable
      .map(
        (i) => `<div class="card"><h4>${escapeHTML(i.path || i.area || "")}</h4><p>${escapeHTML(i.reason)}</p></div>`
      )
      .join("");
  }
  return html || "<p>No reading guidance recorded.</p>";
}

function renderUncertainties(w) {
  return `<p class="summary">These are the things the walkthrough could not establish from the repository. They are stated rather than guessed.</p>` +
    (w.uncertainties || [])
      .map(
        (u) => `<div class="uncertainty">
          <div class="q">${escapeHTML(u.question)}</div>
          ${u.known ? `<div class="detail">Known: ${escapeHTML(u.known)}</div>` : ""}
          ${u.unknown ? `<div class="detail">Unknown: ${escapeHTML(u.unknown)}</div>` : ""}
          ${u.where_next ? `<div class="detail">Where to look: ${escapeHTML(u.where_next)}</div>` : ""}
        </div>`
      )
      .join("");
}

function renderEvidence(w) {
  const meta = w.meta || {};
  const stages = Object.entries(meta.stages || {})
    .map(([role, backend]) => `<span class="chip">${escapeHTML(role)}: ${escapeHTML(backend)}</span>`)
    .join("");
  const notes = (meta.notes || []).map((n) => `<li>${escapeHTML(n)}</li>`).join("");
  const evidence = (w.evidence || [])
    .map((e) => `<div class="card"><h4>${escapeHTML(e.ref)}</h4><p>${escapeHTML(e.summary)}</p></div>`)
    .join("");
  return `
    <p class="summary">How this walkthrough was produced, and what it was based on.</p>
    <div class="chips">${stages}</div>
    ${notes ? `<ul>${notes}</ul>` : ""}
    <h3>Supporting evidence</h3>
    ${evidence || "<p>No evidence entries recorded.</p>"}`;
}

/* ------------------------------------------------------------------ feedback */

function renderFeedback() {
  return `<div class="feedback" id="feedback">
    <p>Did this walkthrough give you the mental model you needed before reading the code?</p>
    <div class="options">
      <button data-answer="yes">Yes</button>
      <button data-answer="mostly">Mostly</button>
      <button data-answer="no">No</button>
    </div>
  </div>`;
}

function wireFeedback(container) {
  const box = container.querySelector("#feedback");
  if (!box) return;
  box.querySelectorAll("button").forEach((button) =>
    button.addEventListener("click", async () => {
      try {
        await api.post(`/api/v1/walkthroughs/${state.runId}/feedback`, { answer: button.dataset.answer });
        box.innerHTML = "<p>Recorded — thank you.</p>";
      } catch (err) {
        box.innerHTML = `<p class="error">${escapeHTML(err.message)}</p>`;
      }
    })
  );
}

/* ------------------------------------------------------------------ source view */

function wireRefs(container) {
  container.querySelectorAll(".ref").forEach((button) =>
    button.addEventListener("click", () => showSource(button.dataset))
  );
}

async function showSource({ path, line, end, side }) {
  const modal = el("source-modal");
  el("source-title").textContent = path;
  el("source-content").textContent = "Loading…";
  modal.classList.remove("hidden");
  try {
    const query = new URLSearchParams({ path });
    if (side) query.set("side", side);
    const data = await api.get(`/api/v1/walkthroughs/${state.runId}/source?${query}`);
    const start = Number(line) || 0;
    const finish = Number(end) || start;
    const lines = data.content.split("\n").map((text, i) => {
      const number = String(i + 1).padStart(4, " ");
      const html = `${number}  ${escapeHTML(text)}`;
      return i + 1 >= start && i + 1 <= finish && start > 0 ? `<span class="highlight">${html}</span>` : html;
    });
    el("source-content").innerHTML = lines.join("\n");
    if (start > 0) {
      const highlighted = el("source-content").querySelector(".highlight");
      highlighted?.scrollIntoView({ block: "center" });
    }
  } catch (err) {
    el("source-content").textContent = err.message;
  }
}

/* ------------------------------------------------------------------ chat */

async function loadConversation() {
  const log = el("chat-log");
  log.innerHTML = "";
  try {
    const data = await api.get(`/api/v1/walkthroughs/${state.runId}/conversation`);
    for (const turn of data.conversation || []) appendMessage(turn.role, turn.content);
  } catch {
    /* a run with no conversation yet is normal */
  }
}

function appendMessage(role, content) {
  const log = el("chat-log");
  const div = document.createElement("div");
  div.className = `msg ${role}`;
  div.innerHTML = role === "assistant" ? renderMarkdown(content) : escapeHTML(content);
  log.appendChild(div);
  log.scrollTop = log.scrollHeight;
  return div;
}

async function askQuestion(event) {
  event.preventDefault();
  const input = el("chat-input");
  const question = input.value.trim();
  if (!question || !state.runId) return;
  input.value = "";
  appendMessage("user", question);
  const pending = appendMessage("assistant", "Looking…");
  pending.classList.add("pending");
  try {
    const res = await api.post(`/api/v1/walkthroughs/${state.runId}/questions`, { question });
    pending.classList.remove("pending");
    pending.innerHTML = renderMarkdown(res.answer);
  } catch (err) {
    pending.classList.remove("pending");
    pending.innerHTML = `<span class="error">${escapeHTML(err.message)}</span>`;
  }
}

/* ------------------------------------------------------------------ wiring */

function toggleChat(open) {
  el("chat-panel").classList.toggle("hidden", !open);
  el("chat-toggle").classList.toggle("hidden", open || !state.runId);
  el("app").classList.toggle("chat-open", open);
}

function wireForm() {
  const type = el("wt-type");
  const selector = el("wt-selector");
  const sync = () => {
    const isChange = type.value === "change";
    document.querySelectorAll('[data-when="change"]').forEach((node) => node.classList.toggle("hidden", !isChange));
    el("range-field").classList.toggle("hidden", !isChange || selector.value !== "range");
    el("base-field").classList.toggle("hidden", !isChange || selector.value === "range");
  };
  type.addEventListener("change", sync);
  selector.addEventListener("change", sync);
  sync();

  el("generate").addEventListener("click", generate);
  el("chat-form").addEventListener("submit", askQuestion);
  el("chat-toggle").addEventListener("click", () => toggleChat(true));
  el("chat-close").addEventListener("click", () => toggleChat(false));
  el("source-close").addEventListener("click", () => el("source-modal").classList.add("hidden"));
  el("source-modal").addEventListener("click", (event) => {
    if (event.target.id === "source-modal") el("source-modal").classList.add("hidden");
  });
  el("chat-input").addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) el("chat-form").requestSubmit();
  });
  document.addEventListener("keydown", (event) => {
    if (event.target.matches("input, textarea")) return;
    if (state.tab !== "walkthrough" || !state.walkthrough) return;
    if (event.key === "ArrowRight" || event.key === "j") el("next-step")?.click();
    if (event.key === "ArrowLeft" || event.key === "k") el("prev-step")?.click();
  });
}

async function main() {
  wireForm();
  await Promise.all([loadRepository(), loadRuns()]);
  if (state.runs.length) await openWalkthrough(state.runs[0].id);
}

main();
