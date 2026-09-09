const listEl = document.getElementById("list");
const detailEl = document.getElementById("detail");
const moreEl = document.getElementById("more");
const cardsEl = document.getElementById("cards");
const filters = document.getElementById("filters");
let cursor = null;
let offset = 0;
let currentQuery = "";
let debounceTimer = null;
let abort = null;

document.getElementById("tab-mail").onclick = () => show("mail");
document.getElementById("tab-stats").onclick = () => {
  show("stats");
  loadStats();
};
filters.onsubmit = (e) => {
  e.preventDefault();
  clearTimeout(debounceTimer);
  resetAndLoad();
};
filters.q.addEventListener("input", () => {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(resetAndLoad, 250);
});
filters.unread.onchange = resetAndLoad;
moreEl.onclick = () => {
  if (searchBox()) offset = listEl.querySelectorAll(".row").length;
  loadList();
};
document.getElementById("sync-now").onclick = async () => {
  const btn = document.getElementById("sync-now");
  btn.disabled = true;
  const r = await api("/api/sync", { method: "POST", body: "{}" });
  if (!r.ok) {
    const j = await r.json().catch(() => ({}));
    alert(j.error || "sync failed");
  }
  loadStatus();
};

document.getElementById("duck-ui").onclick = async () => {
  const r = await api("/api/duckdb-ui", { method: "POST" });
  const j = await r.json();
  if (j.url) window.open(j.url, "_blank");
  else alert(j.error || "failed");
};

function searchBox() {
  return filters.q.value.trim();
}

function resetAndLoad() {
  cursor = null;
  offset = 0;
  listEl.innerHTML = "";
  loadList();
}

function show(which) {
  document.getElementById("mail").hidden = which !== "mail";
  document.getElementById("stats").hidden = which !== "stats";
  document.getElementById("tab-mail").classList.toggle("active", which === "mail");
  document.getElementById("tab-stats").classList.toggle("active", which === "stats");
}

function qs() {
  const p = new URLSearchParams();
  const q = searchBox();
  if (q) p.set("q", q);
  if (filters.unread.checked) p.set("unread", "1");
  if (q) {
    if (offset) p.set("offset", String(offset));
  } else if (cursor) {
    p.set("after_date", cursor.internal_date);
    p.set("after_id", cursor.id);
  }
  return p.toString();
}

function api(path, opts) {
  const u = new URL(path, location.origin);
  return fetch(u, { credentials: "same-origin", ...opts });
}

async function loadList() {
  if (abort) abort.abort();
  abort = new AbortController();
  const signal = abort.signal;
  let j;
  try {
    const r = await api("/api/messages?" + qs(), { signal });
    j = await r.json();
    if (!r.ok) {
      if (!listEl.querySelector(".row")) listEl.textContent = "Search failed.";
      moreEl.hidden = true;
      return;
    }
  } catch (err) {
    if (err.name === "AbortError") return;
    if (!listEl.querySelector(".row")) listEl.textContent = "Search failed.";
    moreEl.hidden = true;
    return;
  }
  if (signal.aborted) return;
  const msgs = j.messages || [];
  if (msgs.length === 0) {
    if (!listEl.querySelector(".row")) listEl.textContent = "No matches.";
    moreEl.hidden = true;
    return;
  }
  for (const m of msgs) {
    const b = document.createElement("button");
    b.className = "row";
    b.type = "button";
    b.innerHTML = `<strong></strong><small></small>`;
    b.querySelector("strong").textContent = m.subject || "(no subject)";
    b.querySelector("small").textContent = (m.from_email || "") + " · " + (m.internal_date || "");
    b.onclick = () => openMsg(m.id, b);
    listEl.appendChild(b);
    cursor = m;
  }
  moreEl.hidden = msgs.length < 50;
}

async function openMsg(id, row) {
  for (const el of listEl.querySelectorAll(".row")) el.classList.remove("active");
  if (row) row.classList.add("active");
  const r = await api("/api/messages/" + encodeURIComponent(id) + "?body=1");
  const j = await r.json();
  const m = j.message || j;
  detailEl.innerHTML = "";
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = [m.from_email, m.subject, m.internal_date, (m.label_ids || []).join(", ")].join("\n");
  let text;
  if (m.has_body) {
    text = m.body;
  } else if (m.body_fetched) {
    text = "No text body";
  } else {
    text = (m.snippet || "") + "\n\n(body not synced)";
  }
  let body;
  if (m.has_body && m.is_html) {
    body = document.createElement("div");
    body.className = "body-html";
    body.innerHTML = text;
  } else {
    body = document.createElement("pre");
    body.textContent = text;
  }
  detailEl.append(meta, body);
  if (!m.has_body && !m.body_fetched) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = "Fetch body";
    btn.onclick = async () => {
      const p = await api("/api/messages/" + encodeURIComponent(id) + "/body", { method: "POST" });
      if (!p.ok) {
        alert((await p.json()).error || "fetch failed");
        return;
      }
      openMsg(id, row);
    };
    detailEl.appendChild(btn);
  }
}

async function loadStats() {
  if (currentQuery === "loaded") return;
  const r = await api("/api/stats");
  const j = await r.json();
  cardsEl.innerHTML = "";
  for (const s of j.stats || []) {
    const card = document.createElement("section");
    const h = document.createElement("h2");
    h.textContent = s.name;
    card.appendChild(h);
    const d = await api("/api/stats/" + encodeURIComponent(s.id));
    const t = await d.json();
    card.appendChild(table(t));
    cardsEl.appendChild(card);
  }
  currentQuery = "loaded";
}

function table(t) {
  const el = document.createElement("table");
  const head = document.createElement("tr");
  for (const c of t.columns || []) {
    const th = document.createElement("th");
    th.textContent = c;
    head.appendChild(th);
  }
  el.appendChild(head);
  for (const row of t.rows || []) {
    const tr = document.createElement("tr");
    for (const cell of row) {
      const td = document.createElement("td");
      td.textContent = cell;
      tr.appendChild(td);
    }
    el.appendChild(tr);
  }
  return el;
}

async function loadStatus() {
  const el = document.getElementById("sync-status");
  const btn = document.getElementById("sync-now");
  try {
    const r = await api("/api/status");
    const s = await r.json();
    if (!r.ok) {
      el.textContent = s.error || "status failed";
      return;
    }
    const parts = [];
    if (s.last_sync) parts.push("last " + s.last_sync);
    if (s.phase) parts.push(s.phase);
    if (s.processed) parts.push(s.processed + " msgs");
    if (s.body_coverage) {
      parts.push("search " + (s.body_coverage.search_covers || "metadata"));
      parts.push(s.body_coverage.with_body + "/" + s.body_coverage.total + " bodies");
    }
    if (s.last_error) parts.push("error: " + s.last_error);
    el.textContent = parts.join(" · ") || "ready";
    const busy = s.phase && s.phase !== "idle";
    btn.disabled = busy;
    document.getElementById("duck-ui").hidden = !s.duckdb_ui;
  } catch (err) {
    el.textContent = "status unavailable";
  }
}

loadList();
loadStatus();
setInterval(loadStatus, 2000);
