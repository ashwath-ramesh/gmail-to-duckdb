const token = new URLSearchParams(location.search).get("t") || "";
const listEl = document.getElementById("list");
const detailEl = document.getElementById("detail");
const moreEl = document.getElementById("more");
const cardsEl = document.getElementById("cards");
let cursor = null;
let currentQuery = "";

document.getElementById("tab-mail").onclick = () => show("mail");
document.getElementById("tab-stats").onclick = () => {
  show("stats");
  loadStats();
};
document.getElementById("filters").onsubmit = (e) => {
  e.preventDefault();
  cursor = null;
  listEl.innerHTML = "";
  loadList();
};
moreEl.onclick = () => loadList();
document.getElementById("duck-ui").onclick = async () => {
  const r = await api("/api/duckdb-ui", { method: "POST" });
  const j = await r.json();
  if (j.url) window.open(j.url, "_blank");
  else alert(j.error || "failed");
};

function show(which) {
  document.getElementById("mail").hidden = which !== "mail";
  document.getElementById("stats").hidden = which !== "stats";
  document.getElementById("tab-mail").classList.toggle("active", which === "mail");
  document.getElementById("tab-stats").classList.toggle("active", which === "stats");
}

function qs() {
  const f = document.getElementById("filters");
  const p = new URLSearchParams();
  if (f.q.value) p.set("q", f.q.value);
  if (f.from.value) p.set("from", f.from.value);
  if (f.label.value) p.set("label", f.label.value);
  if (f.unread.checked) p.set("unread", "1");
  if (cursor) {
    p.set("after_date", cursor.internal_date);
    p.set("after_id", cursor.id);
  }
  return p.toString();
}

function api(path, opts) {
  const u = new URL(path, location.origin);
  if (token) u.searchParams.set("t", token);
  return fetch(u, opts);
}

async function loadList() {
  const r = await api("/api/messages?" + qs());
  const j = await r.json();
  for (const m of j.messages || []) {
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
  moreEl.hidden = !(j.messages && j.messages.length >= 50);
}

async function openMsg(id, row) {
  for (const el of listEl.querySelectorAll(".row")) el.classList.remove("active");
  if (row) row.classList.add("active");
  const r = await api("/api/messages/" + encodeURIComponent(id));
  const m = await r.json();
  detailEl.innerHTML = "";
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = [m.from_email, m.subject, m.internal_date, (m.label_ids || []).join(", ")].join("\n");
  const body = document.createElement("pre");
  body.textContent = m.has_body ? m.body : (m.snippet || "") + "\n\n(body not synced)";
  detailEl.append(meta, body);
  if (!m.has_body) {
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

loadList();
