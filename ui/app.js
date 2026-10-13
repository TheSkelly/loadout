"use strict";
const token = location.hash.slice(1);
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

let S = null;          // latest state from the app
let lastRaw = "";
let view = "library";
let AUTO = null; // what the PS5's autoloader folder holds
let filter = "all";
let bsel = new Set();  // games picked on the Build tab
let bsend = new Set(); // of those, the ones to send to the PS5 once built
let bdest = new Map(); // per-game PS5 drive picked on the Build screen
let batchDrive = null; // the "Send to" pick on the Build screen
let drawerKey = null;

const ICON = {
  folder: '<svg viewBox="0 0 24 24"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5H9l2 2h8.5A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z"/></svg>',
  send: '<svg viewBox="0 0 24 24"><path d="M12 16V4M7 9l5-5 5 5"/><path d="M4 15v3.5A1.5 1.5 0 0 0 5.5 20h13a1.5 1.5 0 0 0 1.5-1.5V15"/></svg>',
  trash: '<svg viewBox="0 0 24 24"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12.5A1.5 1.5 0 0 0 8.5 21h7a1.5 1.5 0 0 0 1.5-1.5L18 7M9 7V4.5A1.5 1.5 0 0 1 10.5 3h3A1.5 1.5 0 0 1 15 4.5V7"/></svg>',
  check: '<svg viewBox="0 0 24 24"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>',
  disc: '<svg viewBox="0 0 24 24"><path d="M12 3 3 7.5 12 12l9-4.5z"/><path d="M3 12l9 4.5 9-4.5"/><path d="M3 16.5 12 21l9-4.5"/></svg>',
  arrow: '<svg viewBox="0 0 24 24"><path d="M5 12h14M13 6l6 6-6 6"/></svg>',
  x: '<svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6 6 18"/></svg>',
};

// ---------------- api ----------------
async function api(path, body) {
  const r = await fetch("/api/" + path, {
    method: "POST", headers: { "X-Token": token, "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  const j = await r.json();
  if (j.error) throw new Error(j.error);
  return j.data;
}
async function act(path, body, okMsg) {
  try {
    const d = await api(path, body);
    if (okMsg) toast(typeof okMsg === "function" ? okMsg(d) : okMsg);
    else if (typeof d === "string" && d) toast(d);
    poll(true);
    return d;
  } catch (e) { toast(e.message, true); }
}

let pollTimer;
async function poll(now) {
  clearTimeout(pollTimer);
  try {
    const r = await fetch("/api/state", { method: "POST", headers: { "X-Token": token } });
    const raw = await r.text();
    const j = JSON.parse(raw);
    if (j.data) {
      j.data.build.jobs = j.data.build.jobs || [];
      (j.data.toasts || []).forEach(t => toast(t));
      j.data.toasts = [];
      const cmp = JSON.stringify(j.data);
      if (cmp !== lastRaw) { lastRaw = cmp; S = j.data; render(); }
    }
  } catch (e) { console.error(e); }
  const busy = S && (S.scanning || S.build.running || S.sizing > 0);
  pollTimer = setTimeout(poll, busy ? 450 : 2000);
}

// ---------------- helpers ----------------
const drives = () => (S.ps5 && S.ps5.drives) || [];
const defDrive = () => S.settings.PS5Drive || "/data";
const driveOf = root => drives().find(d => d.root === root);
const driveLabel = root => (driveOf(root) || {}).label || (root === "/data" ? "Internal SSD" : root);
const sendDrive = k => bdest.get(k) || batchDrive || defDrive();
function driveOpts(sel, short) {
  const ds = drives().slice();
  if (!ds.some(d => d.root === sel)) ds.unshift({ root: sel, label: driveLabel(sel), short: driveLabel(sel), free: -1 });
  return ds.map(d => `<option value="${esc(d.root)}" ${d.root === sel ? "selected" : ""}>${esc(short ? d.short : d.label)}${d.free >= 0 ? ", " + size(d.free) + " free" : ""}</option>`).join("");
}
function ps5Stats() {
  if (!S.settings.PS5IP) return "";
  const p = S.ps5 || {};
  if (!p.checked || p.checked.startsWith("0001")) return `<span class="dim">PS5: checking…</span>`;
  if (!p.online) return `<span class="dim" title="${esc(p.err || "")}">PS5 not reachable</span>`;
  if (!p.shell) return `<button class="link" data-act="ps5help" title="${esc(p.spaceErr ? "Couldn't read free space. " + p.spaceErr : "Free space needs shsrv or the PS5Upload helper running on the PS5")}">PS5 free space?</button>`;
  return drives().filter(d => d.free >= 0).map(d =>
    `<span class="${d.free < 60e9 ? "low" : ""}" title="${esc(d.label)}: ${size(d.free)} free of ${size(d.total)}">${esc(d.short)}: <b>${size(d.free)}</b></span>`).join("");
}
function size(n) {
  if (!n) return "";
  const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0, f = n;
  while (f >= 1024 && i < u.length - 1) { f /= 1024; i++; }
  return (i === 0 ? f : f.toFixed(f >= 100 ? 0 : 1)) + " " + u[i];
}
function dur(s) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, x = s % 60;
  return h ? `${h}h ${String(m).padStart(2, "0")}m` : m ? `${m}m ${String(x).padStart(2, "0")}s` : `${x}s`;
}
function initials(name) {
  return (name || "?").replace(/[^A-Za-z0-9 ]/g, " ").split(/\s+/).filter(Boolean).slice(0, 2).map(w => w[0]).join("").toUpperCase() || "?";
}
function shortVer(v) {
  if (!v) return "";
  const p = v.split(".").map(x => String(parseInt(x, 10)));
  return "v" + (p.length > 2 && p[2] === "0" ? p.slice(0, 2) : p).join(".").replace(/NaN/g, "0");
}
function sdkMajor(s) { return s ? String(parseInt(s, 10)) : ""; }
// ---------------- storefront ----------------
const year = d => (d || "").slice(0, 4);
const artURL = (it, f) => `/art?k=${encodeURIComponent(it.libKey)}&f=${f}`;
function starsHTML(r, n) {
  if (!r) return "";
  const pct = Math.max(0, Math.min(100, r / 5 * 100));
  return `<span class="stars" title="${r.toFixed(2)} out of 5${n ? " from " + n.toLocaleString() + " ratings" : ""}"><span class="s0">★★★★★</span><span class="s1" style="width:${pct}%">★★★★★</span></span><span class="snum">${r.toFixed(1)}</span>`;
}
function byline(it) {
  const parts = [it.publisher, year(it.release), (it.genres || []).slice(0, 2).join(", ")].filter(Boolean);
  return parts.map(esc).join(" · ");
}
let spotIdx = 0, spotTimer = null;
// synopsis turns a store description into one short paragraph for the banner:
// real sentences only, no headings or bullet lists.
function synopsis(desc, max = 460) {
  const lines = (desc || "").split("\n").map(s => s.trim()).filter(Boolean);
  const promo = /limited time|pre-?order|purchase|offer|edition includes|includes:|bonus|upgrade|\bpoints\b|subscription|\*/i;
  let body = lines.filter(s => s.length > 60 && !/^[•\-*·]/.test(s));
  if (body.some(s => !promo.test(s.slice(0, 160)))) body = body.filter(s => !promo.test(s.slice(0, 160)));
  if (!body.length) body = lines;
  let t = body.join(" ");
  if (t.length > max) { t = t.slice(0, max); t = t.slice(0, Math.max(t.lastIndexOf(". ") + 1, t.lastIndexOf(" "))).trim() + (/[.!?]$/.test(t.trim()) ? "" : "…"); }
  return t;
}
// foreign: text that is mostly not in Latin letters (a Japanese store page, say)
function foreign(s) { const t = (s || "").replace(/[\s\d\p{P}]/gu, ""); return t.length > 0 && (t.match(/[^\u0000-\u024f]/g) || []).length > t.length * 0.3; }
// blurb is the banner text: the plain summary when there is one, otherwise the store text
function blurb(it, max) { return it.about ? synopsis(it.about, max) : synopsis(it.desc, max); }
function renderSpot(list) {
  const el = $("#spot");
  const picks = list.filter(i => i.hero).sort((a, b) => (b.rating || 0) - (a.rating || 0));
  if (!picks.length || filter !== "all" || $("#q").value.trim()) { el.hidden = true; return; }
  el.hidden = false;
  spotIdx = spotIdx % picks.length;
  const it = picks[spotIdx];
  const sig = it.key + "|" + it.publisher + "|" + (it.about || it.desc || "").length + "|" + picks.length + "|" + (it.image || "");
  if (el.dataset.sig === sig) return;
  el.dataset.sig = sig;
  el.innerHTML = `<img class="spotart" alt="" src="${artURL(it, "hero")}">
    <div class="spotbody">
      ${it.cover ? `<img class="spotcover" alt="" src="/cover?k=${encodeURIComponent(it.coverKey || it.libKey)}">` : ""}
      <div class="spottext"><span class="eyebrow">${it.present ? "In your library" : "exFAT image"}${it.onPs5 ? " · On PS5" : ""}</span>
        <h2>${esc(it.name || it.current)}</h2>
        <div class="by">${byline(it) || esc(it.titleId || "")}${it.rating ? " " + starsHTML(it.rating, it.ratings) : ""}</div>
        ${blurb(it) ? `<p title="${esc(blurb(it, 2000))}">${esc(blurb(it))}</p>` : ""}
        <div class="spotbtns"><button class="btn primary" data-act="open" data-k="${esc(it.key)}">View game</button>${buildBtnHTML(it, "")}</div></div>
    </div>
    ${picks.length > 1 ? `<div class="spotdots">${picks.map((p, i) => `<button data-spot="${i}" aria-label="Show ${esc(p.name)}" aria-pressed="${i === spotIdx}"></button>`).join("")}</div>` : ""}`;
  clearTimeout(spotTimer);
  spotTimer = setTimeout(() => { spotIdx++; $("#spot").dataset.sig = ""; renderLibrary(); }, 12000);
}
function storeHTML(it) {
  const rows = [];
  if (it.publisher || it.release || (it.genres || []).length || it.rating) {
    rows.push(`<div class="storeline">${byline(it)}${it.rating ? ` <span class="sep"></span>${starsHTML(it.rating, it.ratings)}${it.ratings ? `<span class="muted">(${it.ratings.toLocaleString()})</span>` : ""}` : ""}${it.rated ? `<span class="rated">${esc(it.rated)}</span>` : ""}</div>`);
  }
  if ((it.genres || []).length) rows.push(`<div class="genres">${it.genres.map(g => `<span>${esc(g)}</span>`).join("")}</div>`);
  if (it.about) rows.push(`<div class="desc about">${esc(it.about)}${it.aboutUrl ? ` <button class="linkbtn srclink" data-act="storepage" data-u="${esc(it.aboutUrl)}">From Wikipedia</button>` : ""}</div>`);
  if (it.desc && !(it.about && foreign(it.desc))) {
    const long = it.desc.length > 320;
    rows.push(`<div class="desc${long ? " clamp" : ""}" id="gdesc">${esc(it.desc).replace(/\n/g, "<br>")}</div>${long ? `<button class="more" data-act="moredesc">Show more</button>` : ""}`);
  }
  if (it.shots) rows.push(`<div class="shots">${Array.from({ length: it.shots }, (_, n) => `<button data-act="shot" data-k="${esc(it.key)}" data-n="${n}"><img loading="lazy" alt="Screenshot ${n + 1}" src="${artURL(it, "shot" + n)}"></button>`).join("")}</div>`);
  const links = [];
  if (it.storeUrl) links.push(`<button class="linkbtn" data-act="storepage" data-u="${esc(it.storeUrl)}">PlayStation Store page</button>`);
  if (it.libKey && S.settings.StoreInfo !== false) links.push(`<button class="linkbtn" data-act="refreshinfo" data-k="${esc(it.libKey)}">Refresh info</button>`);
  if (!it.storeDone && it.libKey && S.settings.StoreInfo !== false) rows.push(`<p class="muted small">Looking up store info…</p>`);
  else if (it.storeErr && !it.publisher) rows.push(`<p class="muted small">No store info found (${esc(it.storeErr)}).</p>`);
  if (links.length) rows.push(`<div class="storelinks">${links.join("")}</div>`);
  return rows.length ? `<section class="storeinfo">${rows.join("")}</section>` : "";
}

function coverHTML(it, big) {
  const ini = `<div class="initials">${esc(initials(it.name))}</div>`;
  if (!it.cover) return ini;
  return `${ini}<img loading="lazy" alt="" src="/cover?k=${encodeURIComponent(it.coverKey || it.libKey)}" onerror="this.remove()">`;
}
function busy(it) { const j = jobFor(it.key); return j && ["building", "queued", "verifying", "removing", "sending"].includes(j.state); }
function jobFor(key) { return [...S.build.jobs].reverse().find(j => j.key === key); }
function games() { return S.items.filter(i => (!i.isFile || i.isPkg) && !i.isDlc); }
function dlcCount(it) { return (it.dlc || []).length + (it.incoming || []).length + (it.loose || []).length; }

// ---------------- render ----------------
function render() {
  $("#ver").textContent = "v" + S.version;
  $("#folderPath").textContent = S.settings.Folder || "Choose your games folder";
  $("#rescan").disabled = !S.settings.Folder || S.scanning || S.build.building;
  const present = games().filter(i => i.present || i.exfatOnly);
  const total = present.reduce((a, i) => a + (i.size || 0), 0);
  const built = games().filter(i => i.image).length;
  $("#stats").innerHTML = S.scanning ? "<span>Scanning…</span>" :
    `${S.sizing ? `<span class="measuring">Measuring sizes, ${S.sizing} left</span>` : ""}<span><b>${present.length}</b>games</span><span><b>${size(total) || "0 B"}</b>on disk</span><span><b>${built}</b>with an image</span>`;
  $("#ps5row").innerHTML = ps5Stats();
  const renN = S.items.filter(i => i.canRename).length;
  $("#nav-library").textContent = present.length || "";
  $("#nav-rename").textContent = renN || "";
  const sj = S.build.jobs.filter(x => x.send);
  const buildPct = overallPct();
  const sendPct = sj.length ? sj.reduce((a, x) => a + (["sent", "failed", "cancelled"].includes(x.sendState) ? 1000 : x.sendState === "sending" ? x.sendPct : 0), 0) / sj.length : 1000;
  $("#nav-build").textContent = S.build.running ? Math.round((buildPct >= 1000 && sj.length ? sendPct : buildPct) / 10) + "%" : "";
  $("#railfoot").textContent = S.scanErr ? "Scan failed: " + S.scanErr : "";
  renderLibrary(); renderRename(); renderPCDrives(); renderBuild(); renderPS5(); renderSaves(); renderFiles(); if (view === "emu") renderEmu(); renderSettings();
  { const n = S.build.jobs.filter(x => x.kind.startsWith("fx") && ["done", "failed", "cancelled"].includes(x.state)).length; if (n !== emuJobsDone) { emuJobsDone = n; if (view === "emu") emuLoad(); } }
  { const n = S.build.jobs.filter(x => x.kind.startsWith("fx") && x.state === "done").length; if (n !== fxJobsDone) { fxJobsDone = n; if (view === "files" || view === "homebrew") fxLoad(); } } renderDock();
  if (drawerKey) renderDrawer();
}

const FILTERS = [
  ["all", "All", i => true],
  ["notbuilt", "No image yet", i => i.present && !i.image && i.canBuild],
  ["built", "Image built", i => !!i.image],
  ["rename", "Needs renaming", i => i.canRename],
  ["loose", "Loose files", i => i.wrap && !i.done],
  ["fw", "Needs newer firmware", i => i.needsFw],
  ["backport", "Backported", i => i.backport],
  ["dlc", "Has DLC", i => dlcCount(i) > 0],
  ["exfatonly", "exFAT only", i => i.exfatOnly],
  ["onps5", "On PS5", i => !!i.onPs5],
  ["notsent", "Built, not sent", i => i.image && !i.onPs5],
  ["missing", "Missing", i => !i.present && !i.image],
];

function renderLibrary() {
  const all = games();
  $("#filters").innerHTML = FILTERS.map(([k, label, fn]) => {
    const n = all.filter(fn).length;
    if (k !== "all" && !n) return "";
    return `<button data-f="${k}" aria-pressed="${filter === k}">${label}<i>${n}</i></button>`;
  }).join("");
  $$(".seg button").forEach(b => b.setAttribute("aria-pressed", b.dataset.layout === (S.settings.View || "grid")));
  const q = $("#q").value.trim().toLowerCase();
  const fn = (FILTERS.find(f => f[0] === filter) || FILTERS[0])[2];
  const list = all.filter(fn).filter(i => !q || (i.name + " " + i.titleId + " " + i.current).toLowerCase().includes(q))
    .sort((a, b) => (b.present - a.present) || (a.name || a.current).localeCompare(b.name || b.current));
  const grid = $("#grid");
  grid.classList.toggle("list", S.settings.View === "list");
  if (!S.settings.Folder) {
    grid.innerHTML = `<div class="empty"><h3>Point Loadout at your games</h3><p>Pick the folder that holds your PS5 game folders. Nothing is changed until you say so.</p><button class="btn primary" data-act="pick">Choose folder</button></div>`;
    return;
  }
  if (!list.length) {
    $("#spot").hidden = true;
    grid.innerHTML = S.scanning ? `<div class="empty"><h3>Scanning…</h3><p>Reading game info from each folder.</p></div>` :
      all.length ? `<div class="empty"><h3>Nothing matches</h3><p>Try a different search or filter.</p></div>` :
      `<div class="empty"><h3>No games found here</h3><p>Game folders need a title ID in the name (like PPSA01234) or a sce_sys folder inside.</p><button class="btn primary" data-act="pick">Choose another folder</button></div>`;
    return;
  }
  renderSpot(list);
  grid.innerHTML = list.map(tileHTML).join("");
}

function ps5Copies(it) {
  const cs = it.onPs5List || [];
  if (!cs.length) return it.onPs5 ? "Sent to " + it.onPs5 : "";
  return cs.map(c => `${c.drive}: ${{ image: "image", folder: "game folder", installed: "installed" }[c.kind] || c.kind}, ${c.path}`).join("\n");
}
function tagsHTML(it, forDrawer) {
  const t = [];
  if (it.isPkg) t.push(`<span class="tag sky" title="A PS4 game package (.pkg). It installs on the PS5 as it is, so there's no image to build.">PS4 PKG</span>`);
  if (it.image) t.push(`<span class="tag mint" title="${esc(it.exfatOnly ? "Converted. The game folder is gone, it lives in this exFAT image now." : "Converted to an exFAT image")}">exFAT</span>`);
  else if (!it.present) t.push(`<span class="tag dim">Missing</span>`);
  if (it.needsFw) t.push(`<span class="tag coral">Needs FW ${esc(it.reqFw)}</span>`);
  else if (forDrawer && it.reqFw) t.push(`<span class="tag dim">FW ${esc(it.reqFw)}</span>`);
  if (it.backport) t.push(`<span class="tag gold" title="This game has already been backported: its folder holds a fakelib folder (or its name says backport). Loadout only labels it.">Backported</span>`);
  if (it.wrap && !it.done) t.push(`<span class="tag sky">Loose files</span>`);
  if (dlcCount(it)) t.push(`<span class="tag lilac">DLC ${dlcCount(it)}</span>`);
  if (it.onPs5) t.push(`<span class="tag sky" title="${esc(ps5Copies(it))}">On PS5</span>`);
  return t.join("");
}

function buildBtnHTML(it, cls = "wide") {
  const j = jobFor(it.key);
  if (j && ["building", "queued", "verifying", "removing", "sending"].includes(j.state))
    return `<button class="btn ${cls}" disabled>${{ queued: "Queued", building: "Building " + Math.floor(j.pct / 10) + "%", verifying: "Checking " + Math.floor(j.pct / 10) + "%", removing: "Removing folder", sending: "Sending " + Math.floor(j.pct / 10) + "%" }[j.state]}</button>`;
  if (it.image && S.settings.PS5IP) return it.onPs5
    ? `<button class="btn onps5 ${cls}" data-act="send1" data-k="${esc(it.key)}" title="Already in ${esc(it.onPs5)} on the PS5. Click to send it again">${ICON.check}On PS5</button>`
    : `<button class="btn sendps5 ${cls}" data-act="send1" data-k="${esc(it.key)}" title="Send ${esc(it.image)} to the PS5">${ICON.send}Send to PS5</button>`;
  if (it.image) return `<button class="btn built ${cls}" data-act="showimg" data-k="${esc(it.key)}" title="${esc(it.image)}">${ICON.check}Image built</button>`;
  if (!it.present) return `<button class="btn ${cls}" disabled>Not in folder</button>`;
  if (it.isPkg) {
    const pj = jobFor(it.key), plive = pj && pj.kind === "pkginst" && ["queued", "sending"].includes(pj.state);
    if (plive) return `<button class="btn ${cls}" disabled>${pj.state === "queued" ? "Queued" : pj.note ? "Starting…" : "Sending " + Math.floor(pj.pct / 10) + "%"}</button>`;
    if (it.onPs5) return `<button class="btn onps5 ${cls}" disabled title="${esc(ps5Copies(it))}">${ICON.check}On PS5</button>`;
    return S.settings.PS5IP ? `<button class="btn sendps5 ${cls}" data-act="pkginstall" data-k="${esc(it.key)}" title="The PS5 downloads this package from your PC and installs it">${ICON.send}Install on PS5</button>`
      : `<button class="btn ${cls}" data-act="showpkg" data-k="${esc(it.key)}" title="Shows the .pkg file in its folder">${ICON.folder}Show PKG file</button>`;
  }
  if (!it.canBuild) return `<button class="btn ${cls}" disabled title="${esc(it.buildNote)}">${esc(it.buildNote || "Can't build")}</button>`;
  return `<button class="btn make ${cls}" data-act="build1" data-k="${esc(it.key)}">${ICON.disc}Build exFAT</button>`;
}

function tileHTML(it) {
  const j = jobFor(it.key);
  const prog = j && ["building", "verifying", "sending"].includes(j.state) ? `<div class="mini ${j.state}"><i style="width:${j.pct / 10}%"></i></div>` : "";
  const ver = shortVer(it.version);
  return `<article class="tile${it.present || it.image ? "" : " missing"}" data-k="${esc(it.key)}">
    <button class="cover" data-act="open" data-k="${esc(it.key)}" aria-label="Details for ${esc(it.name || it.current)}">${coverHTML(it)}<div class="tags">${tagsHTML(it)}</div></button>
    <div class="meta"><h4>${esc(it.name || it.current)}</h4>
      ${it.publisher || it.genres ? `<div class="byl">${[year(it.release), (it.genres || [])[0]].filter(Boolean).map(esc).join(" · ")}${it.rating ? `<span class="star">★ ${it.rating.toFixed(1)}</span>` : ""}</div>` : ""}
      <div class="sub"><span class="id">${esc(it.titleId || "No ID")}</span>${ver ? `<span>${esc(ver)}</span>` : ""}<span>${it.sized ? esc(size(it.size)) : (it.present ? "measuring…" : "")}</span></div>
      <div class="rowtags">${tagsHTML(it)}</div>
      ${dlcCount(it) ? `<button class="dlclink" data-act="dlc" data-k="${esc(it.key)}">View ${dlcCount(it)} DLC</button>` : ""}${saveLink(it)}</div>
    ${prog}
    <div class="actions">${buildBtnHTML(it)}${it.present && !it.isPkg ? `<button class="btn icon" data-act="folder" data-k="${esc(it.key)}" title="Open folder">${ICON.folder}</button>` : ""}${it.present && it.image && !busy(it) && !(it.dlc || []).length ? `<button class="btn icon" data-act="remove1" data-k="${esc(it.key)}" title="Remove the game folder (keeps the image)">${ICON.trash}</button>` : ""}</div>
  </article>`;
}

// ---------------- drawer ----------------
function openDrawer(k) { drawerKey = k; renderDrawer(); $("#drawer").classList.add("open"); $("#scrim").classList.add("open"); $("#drawer").setAttribute("aria-hidden", "false"); $("#drawerClose").focus(); }
function closeDrawer() { drawerKey = null; $("#drawer").classList.remove("open"); $("#scrim").classList.remove("open"); $("#drawer").setAttribute("aria-hidden", "true"); }
function renderDrawer() {
  const it = S.items.find(i => i.key === drawerKey);
  if (!it) { closeDrawer(); return; }
  const f = [
    ["Title ID", `<span class="id">${esc(it.titleId || "Unknown")}</span>`],
    ["Version", esc(it.version || "Unknown")],
    ["Built with SDK", esc(it.sdk || "Unknown")],
    ["Needs firmware", esc(it.reqFw || "Unknown") + (it.needsFw ? ` <span style="color:var(--coral)">(newer than your ${esc(S.settings.ConsoleFW)})</span>` : "")],
    ["Platform", esc(it.platform || "Unknown")],
    ["Content ID", esc(it.contentId || "Unknown")],
    ["Size", it.sized ? esc(size(it.size)) : "Calculating…"],
    [it.isPkg ? "PKG file" : "Folder", it.exfatOnly ? "Removed, the game now lives in its exFAT image" : esc(it.path)],
    ["exFAT image", it.isPkg ? "Not needed, PS4 games install from the PKG" : it.image ? esc(it.image) : "Not built yet"],
    ["On the PS5", it.onPs5List && it.onPs5List.length ? `<ul class="ps5copies">${it.onPs5List.map(c => `<li><span><b>${esc(c.drive)}</b>, ${esc({ image: "image", folder: "game folder", installed: "installed" }[c.kind] || c.kind)}${c.size ? ", " + size(c.size) : ""}<i class="mono">${esc(c.path)}</i></span>${c.kind === "installed" ? `<em title="Installed games are deleted from the PS5's own game menu">delete on the PS5</em>` : `<span class="rowbtns">${c.kind === "image" && drives().length > 1 ? `<button class="btn ghost sm" data-act="ps5move" data-p="${esc(c.path)}">${ICON.arrow}Move</button>` : ""}<button class="btn ghost sm danger" data-act="ps5del" data-k="${esc(it.key)}" data-p="${esc(c.path)}">${ICON.trash}Delete</button></span>`}</li>`).join("")}</ul>` : it.onPs5 ? esc(it.onPs5) : (S.ps5 && S.ps5.online && S.ps5.scanned ? "Not on the PS5" : it.image ? "Not sent yet" : "No image to send")],
    ["Name came from", esc({ "param.json": "Game metadata (param.json)", "param.sfo": "Game metadata (param.sfo)", "nptitle.dat": "nptitle.dat", "saved": "Name you typed", "manual": "Name you typed", "folder name": "Old folder name, worth checking", "library": "Earlier scan", "matched": "Matching game folder" }[it.source] || "Unknown")],
    ["First seen", esc(it.firstSeen)],
  ];
  const src = `/cover?k=${encodeURIComponent(it.coverKey || it.libKey)}`;
  const heroArt = it.hero ? `<img class="art" alt="" src="${artURL(it, "hero")}">` : it.cover ? `<img class="bg" alt="" src="${src}">` : "";
  $("#drawerBody").innerHTML = `<div class="hero${it.hero ? " wide" : ""}">${heroArt}${it.cover ? `<img class="fg" alt="" src="${src}">` : `<div class="initials">${esc(initials(it.name))}</div>`}</div>
    <div class="body"><h2>${esc(it.name || it.current)}</h2>${storeHTML(it)}<div class="tagrow">${tagsHTML(it, true)}</div>
    <dl class="facts">${f.map(([a, b]) => `<dt>${a}</dt><dd>${b}</dd>`).join("")}</dl>
    ${dlcCount(it) ? `<section class="dlcsec"><h3>DLC <span>${dlcCount(it)}</span></h3>${dlcListHTML(it)}</section>` : ""}
    ${it.present ? `<div class="btns">${buildBtnHTML(it, "")}${it.isPkg ? "" : `<button class="btn ghost" data-act="folder" data-k="${esc(it.key)}">${ICON.folder}Open folder</button>`}
      ${it.image ? `<button class="btn ghost" data-act="showimg" data-k="${esc(it.key)}">${ICON.check}Show image</button>` : ""}
      <button class="btn ghost" data-act="rename1" data-k="${esc(it.key)}">Fix name</button>
      ${it.image && !busy(it) && !(it.dlc || []).length ? `<button class="btn ghost danger" data-act="remove1" data-k="${esc(it.key)}">${ICON.trash}Remove game folder</button>` : ""}
      ${it.titleId ? `<button class="btn ghost" data-act="copyid" data-id="${esc(it.titleId)}">Copy title ID</button>` : ""}</div>` : ""}
    </div>`;
}

function dlcListHTML(it) {
  const row = (d, moving) => `<li><div><b>${esc(d.name || d.file)}</b>${moving === "loose" ? `<em>In your games folder. The game itself is an exFAT image</em>` : moving ? `<em>Moves into this folder when you rename</em>` : ""}</div>${d.note ? `<div class="dnote">${esc(d.note)}</div>` : ""}
    <div class="dmeta"><span>${esc(d.kind)}</span>${d.version ? `<span>${esc(shortVer(d.version))}</span>` : ""}${d.size ? `<span>${esc(size(d.size))}</span>` : ""}${d.contentId ? `<span class="id">${esc(d.contentId)}</span>` : ""}</div>
    <div class="dpath" title="${esc(d.path)}">${esc(d.file)}</div></li>`;
  return `<ul class="dlclist">${(it.dlc || []).map(d => row(d, false)).join("")}${(it.incoming || []).map(d => row(d, true)).join("")}${(it.loose || []).map(d => row(d, "loose")).join("")}</ul>`;
}
async function showDLC(it) {
  const first = (it.dlc || [])[0];
  const r = await modal(`<h3>DLC for ${esc(it.name || it.current)}</h3>
    <p>DLC stays in the game's folder and isn't moved into the ${esc(it.titleId || "game")}-app folder. It isn't included in exFAT images.</p>${dlcListHTML(it)}`,
    first ? [{ label: "Close" }, { label: "Open DLC folder", cls: "primary" }] : [{ label: "Close", cls: "primary" }]);
  if (r && first && r.i === 1) act("open", { path: first.path.replace(/[\\/][^\\/]+$/, "") });
}

// ---------------- rename ----------------
// plain-word buttons for the name codes
const GAME_TOKENS = [["{TITLE}", "Game name"], ["{ID}", "Game ID"], ["{VERSION}", "Version"], ["{PLATFORM}", "PS4 / PS5"]];
const DLC_TOKENS = [["{GAME}", "Game name"], ["{ID}", "Game ID"], ["{DLC}", "DLC name"], ["{LABEL}", "DLC code"], ["{VERSION}", "Version"]];
function presetList(name, presets, examples, current, customOpen) {
  const isPreset = presets.includes(current) && !customOpen;
  return presets.map((p, i) => `<label class="pchoice${isPreset && p === current ? " on" : ""}"><input type="radio" name="${name}" value="${esc(p)}" ${isPreset && p === current ? "checked" : ""}><span>${esc(examples[i] || p)}</span></label>`).join("")
    + `<label class="pchoice${!isPreset ? " on" : ""}"><input type="radio" name="${name}" value="" ${!isPreset ? "checked" : ""}><span class="own">Make my own</span></label>`;
}
let gameOwn = false, dlcOwn = false;
function renderRename() {
  const pat = $("#pattern");
  if (document.activeElement !== pat) pat.value = S.settings.Pattern;
  $("#tokens").innerHTML = GAME_TOKENS.map(([t, l]) => `<button data-token="${t}" data-target="pattern" title="Adds ${t}">${l}</button>`).join("");
  const dp = $("#dlcPattern");
  if (document.activeElement !== dp) dp.value = S.settings.DLCPattern || "";
  $("#dlcTokens").innerHTML = DLC_TOKENS.map(([t, l]) => `<button data-token="${t}" data-target="dlcPattern" title="Adds ${t}">${l}</button>`).join("");
  const dex = $("#dlcExample");
  dex.classList.toggle("warn", !!S.dlcProblem);
  dex.innerHTML = S.dlcProblem ? esc(S.dlcProblem) + " Using the default until it's fixed." : `Looks like: <b>${esc(S.dlcExample)}</b>`;
  const ex = $("#example");
  ex.classList.toggle("warn", !!S.patternProblem);
  ex.innerHTML = S.patternProblem ? esc(S.patternProblem) + " Using the default until it's fixed." : `Looks like: <b>${esc(S.example)}</b>`;
  $("#nameSum").innerHTML = `<b>${esc(S.example)}</b>`;
  $("#nameSum").title = "DLC: " + S.dlcExample;
  const gOwn = gameOwn || !S.presets.includes(S.settings.Pattern), dOwn = dlcOwn || !(S.dlcPresets || []).includes(S.settings.DLCPattern);
  if (!$("#nameEdit").hidden && !document.activeElement.matches("#nameEdit input:not([type=radio])")) {
    $("#gamePresets").innerHTML = presetList("gp", S.presets, S.presetEx || [], S.settings.Pattern, gameOwn);
    $("#dlcPresetList").innerHTML = presetList("dp", S.dlcPresets || [], S.dlcPresetEx || [], S.settings.DLCPattern, dlcOwn);
  }
  $("#gameCustom").hidden = !gOwn; $("#dlcCustom").hidden = !dOwn;
  $("#safe").checked = S.safe; $("#wrap").checked = S.wrapLoose; $("#movedlc").checked = S.moveDlc;

  const rows = S.items.filter(i => i.present);
  const sel = rows.filter(i => i.checked);
  const able = rows.filter(i => i.canRename);
  const all = $("#renAll");
  all.checked = able.length > 0 && sel.length === able.length; all.indeterminate = sel.length > 0 && sel.length < able.length;
  $("#renSummary").textContent = rows.length ? `${sel.length} of ${able.length} ready to rename` + (S.items.filter(i => i.wrap && !i.done).length ? `, ${S.items.filter(i => i.wrap && !i.done).length} with loose files` : "") : "";
  $("#undo").disabled = !S.canUndo || S.build.building;
  $("#doRename").disabled = !sel.length || S.build.building;
  $("#renList").innerHTML = rows.length ? rows.map(i => {
    let cls = "", st = i.status || "";
    if (i.done) cls = "ok"; else if (/^Failed|No name/.test(st)) cls = "bad"; else if (/check it|files into/.test(st)) cls = "warn";
    if (i.isDlc) {
      if (i.done) cls = "ok"; else if (/^Failed/.test(st)) cls = "bad"; else if (i.canRename) cls = "warn"; else cls = "";
      const parts = (i.newName || "").split(/[\\/]/).filter(Boolean);
      const dest = i.dlcFor && parts.length ? `<span class="crumb">DLC</span><b>${esc(parts[1] || "")}</b>${parts[2] ? `<span class="crumb tail">${esc(parts[2])}</span>` : ""}` : "Stays where it is";
      return `<div class="row dlcrow">
      <label class="check"><input type="checkbox" data-ren="${esc(i.key)}" ${i.checked ? "checked" : ""} ${i.canRename ? "" : "disabled"}><span></span></label>
      <span class="old" title="${esc(i.current)}">${esc(i.current)}<span class="kind">${i.inPlace ? "DLC, in game folder" : "DLC"}</span></span>
      <span class="arrow">${ICON.arrow}</span>
      <span class="new dest${i.canRename || i.done ? "" : " same"}" title="${esc(i.newName || "")}">${dest}</span>
      <span class="st ${cls}"><span title="${esc(st)}">${esc(st)}</span></span></div>`;
    }
    const same = !i.done && i.newName === i.current && !i.wrap;
    return `<div class="row">
      <label class="check"><input type="checkbox" data-ren="${esc(i.key)}" ${i.checked ? "checked" : ""} ${i.canRename ? "" : "disabled"}><span></span></label>
      <span class="old" title="${esc(i.current)}">${esc(i.current)}<span class="kind">${i.isFile ? "file" : ""}</span></span>
      <span class="arrow">${ICON.arrow}</span>
      <button class="namebtn new${same ? " same" : ""}" data-act="rename1" data-k="${esc(i.key)}" title="Click to type a different game name">${esc(i.done ? i.current : (i.newName || "Click to type the game's name"))}</button>
      <span class="st ${cls}"><span title="${esc(st)}">${esc(st)}</span></span></div>`;
  }).join("") : `<div class="empty"><p>Scan a folder to see what can be renamed.</p></div>`;
}

// ---------------- build ----------------
function overallPct() {
  const jobs = S.build.jobs.filter(j => j.kind === "build" || j.kind === "remove"); if (!jobs.length) return 0;
  return jobs.reduce((a, j) => a + (["done", "failed", "skipped", "cancelled", "removing"].includes(j.state) ? 1000 : j.state === "queued" ? 0 : j.pct), 0) / jobs.length;
}
// a row can be ticked if it can be built, or already has an image to send
const pickable = i => i.canBuild || !!i.image;
// " (Ext 1)" style hint for where a game sits on the PS5
const ps5Where = i => { const d = [...new Set((i.onPs5List || []).map(c => c.drive))]; return d.length ? ` (${d.join(", ")})` : ""; };
let showDone = false;
function renderBuild() {
  $("#outPath").textContent = S.outDir || "Choose a folder";
  $("#freeOut").textContent = S.freeOut > 0 ? size(S.freeOut) + " free" : "";
  const allRows = games().filter(i => (i.present || i.exfatOnly) && !i.isPkg)
    .sort((a, b) => (b.present - a.present) || (a.name || a.current).localeCompare(b.name || b.current));
  // built and already on the PS5: nothing left to do, so these stay out of the way
  // (a game from the current run stays visible so its result can be read)
  const finished = i => !!i.image && !!i.onPs5 && !jobFor(i.key);
  const nDone = allRows.filter(finished).length;
  const rows = showDone ? allRows : allRows.filter(i => !finished(i));
  const sd = $("#bldShowDone");
  sd.hidden = !nDone; sd.textContent = showDone ? "Hide games already on the PS5" : `Show ${nDone} already on the PS5`;
  for (const k of [...bsel]) if (!rows.some(r => r.key === k && pickable(r))) bsel.delete(k);
  for (const k of [...bsend]) if (!bsel.has(k)) bsend.delete(k);
  const canSend = !!S.settings.PS5IP;
  const sa = $("#bldSendAll");
  sa.disabled = !canSend || !bsel.size || S.build.building;
  sa.checked = bsel.size > 0 && bsend.size === bsel.size; sa.indeterminate = bsend.size > 0 && bsend.size < bsel.size;
  sa.parentElement.title = canSend ? "Send the selected games to the PS5 as they finish building" : "Set your PS5's IP address in Settings to send games";
  const able = rows.filter(pickable);
  const all = $("#bldAll");
  all.checked = able.length > 0 && bsel.size === able.length; all.indeterminate = bsel.size > 0 && bsel.size < able.length;
  all.disabled = false;
  const selSize = rows.filter(i => bsel.has(i.key)).reduce((a, i) => a + (i.size || 0), 0);
  const tooBig = S.freeOut > 0 && selSize > S.freeOut;
  const need = {};
  rows.filter(i => bsend.has(i.key)).forEach(i => { const r = sendDrive(i.key); need[r] = (need[r] || 0) + (i.size || 0); });
  const full = Object.entries(need).map(([r, n]) => [driveOf(r), n]).filter(([d, n]) => d && d.free >= 0 && n > d.free)
    .map(([d, n]) => `${esc(d.short)} needs ${size(n)} but has ${size(d.free)}`);
  $("#bldSummary").innerHTML = bsel.size ? `${bsel.size} selected, about ${size(selSize)}` + (bsend.size ? `, ${bsend.size} going to the PS5` : "") + (tooBig ? ` <span style="color:var(--coral)">which is more than the free space</span>` : "")
    + (full.length ? ` <span style="color:var(--coral)">Not enough room on the PS5: ${full.join(", ")}</span>` : "") : (S.build.summary ? esc(S.build.summary) : "");
  const dsel = $("#bldDest");
  $("#bldDestWrap").hidden = !canSend;
  if (document.activeElement !== dsel) dsel.innerHTML = driveOpts(batchDrive || defDrive());
  dsel.disabled = false;
  const buildN = rows.filter(i => bsel.has(i.key) && i.canBuild).length;
  // the start buttons turn into Stop buttons while their lane runs
  const db = $("#doBuild");
  if (S.build.building) { db.disabled = false; db.textContent = "Stop building"; db.className = "btn danger solid"; db.dataset.mode = "stop"; }
  else { db.disabled = !buildN; db.textContent = buildN > 1 ? `Build ${buildN} images` : "Build selected"; db.className = "btn primary"; db.dataset.mode = ""; }
  $("#bldMissing").disabled = false;
  const sendable = games().filter(i => bsel.has(i.key) && i.image).length;
  const ds = $("#doSend");
  if (S.build.ps5Busy) { ds.disabled = false; ds.textContent = "Stop sending"; ds.className = "btn danger solid"; ds.dataset.mode = "stop"; }
  else { ds.disabled = !sendable; ds.textContent = sendable > 1 ? `Send ${sendable} to PS5` : "Send selected to PS5"; ds.className = "btn ghost"; ds.dataset.mode = ""; }
  $("#removeAfter").checked = !!S.settings.RemoveAfter;
  $("#nukeBig").checked = !!S.settings.NukeBig; $("#nukeBigRow").hidden = !S.settings.RemoveAfter;
  if (document.activeElement && document.activeElement.matches("#bldList select")) return;
  const pickDrive = canSend && drives().length > 1;
  $("#bldList").innerHTML = rows.length ? rows.map(i => {
    const j = jobFor(i.key);
    let st, cls = "";
    if (j) {
      cls = { done: "ok", failed: "bad", building: "warn", verifying: "warn", removing: "warn", sending: "warn", skipped: "", cancelled: "" }[j.state] || "";
      if (j.state === "done" && /kept/i.test(j.note)) cls = "warn";
      const plain = j.kind === "remove" || j.kind === "send";
      st = { queued: "Queued", building: j.note || "Building", verifying: "Checking image", removing: "Moving folder to the Recycle Bin", sending: j.note || "Sending", done: (plain ? "" : "Built, ") + j.note, failed: (plain ? "" : "Failed: ") + j.note, skipped: "Skipped, image already exists", cancelled: "Cancelled" }[j.state];
      if ((j.state === "building" && !j.note) || j.state === "verifying" || (j.state === "sending" && !j.note)) st = `<span class="bar ${j.state}"><i style="width:${j.pct / 10}%"></i></span><span>${{ verifying: "Checking ", sending: "Sending " }[j.state] || ""}${Math.floor(j.pct / 10)}%</span>`;
      else st = `<span title="${esc(st)}">${esc(st)}</span>`;
      if (j.send) {
        const sc = { sent: "ok", failed: "bad", cancelled: "dim", waiting: "dim" }[j.sendState] || "";
        let sl;
        if (j.sendState === "sending" && !j.sendNote) sl = `<span class="bar sending"><i style="width:${j.sendPct / 10}%"></i></span><span>Sending ${Math.floor(j.sendPct / 10)}%</span>`;
        else sl = `<span title="${esc(j.sendNote || "")}">${esc({ waiting: "Will send to the PS5" + (j.destLabel ? ", " + j.destLabel : ""), sending: j.sendNote || "Sending", sent: j.sendNote || "Sent to the PS5", failed: "Not sent: " + (j.sendNote || ""), cancelled: j.sendNote || "Not sent" }[j.sendState] || "")}</span>`;
        st = `<span class="line">${st}</span><span class="line send ${sc}">${sl}</span>`;
        cls += " two";
      }
    } else if (i.exfatOnly) { cls = "ok"; st = `<span>exFAT image only${i.onPs5 ? ", on the PS5" + ps5Where(i) : ", ready to send"}</span>`; }
    else if (i.image) { cls = "ok"; st = `<span>Image built${i.onPs5 ? ", on the PS5" + ps5Where(i) : ""}</span>`; }
    else if (!i.canBuild) { cls = "bad"; st = `<span>${esc(i.buildNote)}</span>`; }
    else st = `<span>${esc(i.buildNote || "Ready")}</span>`;
    if (!j && pickDrive && bsel.has(i.key) && (bsend.has(i.key) || !i.canBuild)) st = `<span class="line">${st}</span><span class="line send">to <select class="rowdest" data-bdest="${esc(i.key)}">${driveOpts(sendDrive(i.key), true)}</select></span>`, cls += " two";
    return `<div class="row build">
      <label class="check"><input type="checkbox" data-bld="${esc(i.key)}" ${bsel.has(i.key) ? "checked" : ""} ${pickable(i) && !busy(i) ? "" : "disabled"}><span></span></label>
      <span class="gname">${i.cover ? `<img alt="" src="/cover?k=${encodeURIComponent(i.coverKey || i.libKey)}">` : `<span class="ph"></span>`}<b>${esc(i.name || i.current)}</b></span>
      <span class="size">${i.sized ? esc(size(i.size)) : "…"}</span>
      <span class="new" title="${esc(i.imageName || i.image || "")}">${esc(i.imageName || (i.image || "").split(/[\\/]/).pop())}</span>
      ${i.canBuild ? `<label class="mini-switch" title="${canSend ? "Send to the PS5 when it's built" : "Set your PS5's IP in Settings first"}"><input type="checkbox" data-bsend="${esc(i.key)}" ${bsend.has(i.key) ? "checked" : ""} ${canSend && bsel.has(i.key) && !busy(i) ? "" : "disabled"}><span></span></label>` : `<span class="nosw" title="Already an image. Tick it and use Send to PS5 below.">–</span>`}
      <span class="st ${cls}">${st}</span></div>`;
  }).join("") : nDone ? `<div class="empty"><p>Every game here is built and already on the PS5. Nothing left to do.</p></div>` : `<div class="empty"><p>Scan a folder to see games you can build.</p></div>`;
}

function renderPCDrives() {
  const el = $("#pcDrives"), ds = S.pcDrives || [];
  if (!ds.length) { el.innerHTML = ""; return; }
  el.innerHTML = `<span class="pclabel" title="Build straight onto a drive at full disk speed, then plug it into the PS5. ShadowMount+ finds images in etaHEN\\games.">PS5 drives on this PC</span>` +
    ds.map(d => {
      const name = `${esc(d.label || "Drive")} (${esc(d.letter)}:)`;
      const tip = `${d.label || "Drive"} (${d.letter}:), ${d.fs}, ${size(d.free)} free of ${size(d.total)}`;
      if (!d.ok) return `<span class="pcchip bad" title="${esc(tip + ". " + d.problem)}">${name} <i>${esc(d.fs)}, can't use</i></span>`;
      if (d.inUse) return `<button class="pcchip on" data-pcopen="${esc(d.folder)}" title="${esc(tip)}. Images are being saved here. Click to open the folder.">${ICON.check}${name} <i>${size(d.free)} free</i></button>`;
      if (d.ready) return `<button class="pcchip" data-pcuse="${esc(d.root)}" ${S.build.building ? "disabled" : ""} title="${esc(tip)}. Click to save images here.">${name} <i>${size(d.free)} free, use it</i></button>`;
      return `<button class="pcchip setup" data-pcsetup="${esc(d.root)}" title="${esc(tip)}">${name} <i>set up for PS5</i></button>`;
    }).join("");
}

// ---------------- emulators ----------------
let EM = { groups: null, open: new Set(), apps: [], busy: false, err: "", loaded: false, cur: "", slot: "games", games: [], gbusy: false, gmissing: false };
let emuView = "tiles";
try { emuView = localStorage.getItem("emuView") || "tiles"; } catch (e) { /* storage may be off; the default is fine */ }
const GRID_SVG = `<svg viewBox="0 0 24 24"><rect x="4" y="4" width="7" height="7" rx="1"/><rect x="13" y="4" width="7" height="7" rx="1"/><rect x="4" y="13" width="7" height="7" rx="1"/><rect x="13" y="13" width="7" height="7" rx="1"/></svg>`;
const LIST_SVG = `<svg viewBox="0 0 24 24"><path d="M4 6h16M4 12h16M4 18h16"/></svg>`;
function initials(s) { const w = (s || "?").replace(/\[[^\]]*\]|\([^)]*\)/g, " ").trim().split(/\s+/).filter(Boolean); return ((w[0] || "?")[0] + (w[1] ? w[1][0] : "")).toUpperCase(); }
function emuSlot(app) { return (app.slots || []).find(s => s.key === EM.slot) || (app.slots || [])[0]; }
async function emuLoad() {
  EM.busy = true; EM.err = ""; renderEmu();
  try { EM.apps = (await api("emu/list", {})).apps || []; EM.loaded = true; } catch (e) { EM.err = e.message; }
  EM.busy = false;
  if (EM.cur && !EM.apps.some(a => a.path === EM.cur)) EM.cur = "";
  renderEmu();
  if (EM.cur) emuGames();
}
async function emuGames() {
  const app = EM.apps.find(a => a.path === EM.cur), sl = app && emuSlot(app); if (!sl) { EM.games = []; renderEmu(); return; }
  EM.gbusy = true; renderEmu();
  try { const d = await api("emu/games", { Dir: sl.dir }); EM.games = d.games || []; EM.groups = d.groups || null; EM.gmissing = !!d.missing; } catch (e) { EM.games = []; toast(e.message, true); }
  EM.gbusy = false; renderEmu();
  if (app && FWD_OK.includes(app.folder) && sl.key === "games") fwdLoad();
  if (app && sl.key === "games") cheatsLoad(app);
}
// which games have cheats or mods installed: game ID -> how many files
function gameIdOf(name) { const m = (name || "").match(/[\[(]([0-9A-Fa-f]{16})[\])]/); return m ? m[1].toUpperCase() : ""; }
function modsDirOf(app) { const s = app && (app.slots || []).find(x => x.key === "mods"); return s ? s.dir : ""; }
async function cheatsLoad(app) {
  const dir = modsDirOf(app); EM.cheats = {}; if (!dir) return;
  const ids = [...new Set((EM.games || []).map(g => gameIdOf(g.name)).filter(Boolean))]; if (!ids.length) return;
  try { EM.cheats = await api("emu/cheats/summary", { Dir: dir, IDs: ids }) || {}; renderEmu(); } catch (e) { /* the tags just don't show */ }
}
function cheatTag(g) { const id = gameIdOf(g.name), n = id && EM.cheats && EM.cheats[id]; return n ? `<button class="tag lilac tagbtn" data-emucheats="${esc(id)}" data-title="${esc(g.title || g.name)}" title="See the cheats and mods installed for this game">Cheats ${n}</button>` : ""; }
async function cheatsShow(app, id, title) {
  let mods;
  try { mods = await api("emu/cheats/show", { Dir: modsDirOf(app), ID: id }); } catch (err) { return toast(err.message, true); }
  const body = (mods || []).map(m => `<div class="cheatmod"><h4>${esc(m.name)}</h4>
      ${m.patches ? `<p>${m.patches} code patch file${m.patches === 1 ? "" : "s"}</p>` : ""}${m.files ? `<p>Replacement game files</p>` : ""}
      ${(m.sets || []).map(s => `<div class="cheatset">${s.file ? `<small class="mono">For game build ${esc(s.file)}</small>` : ""}${(s.cheats || []).length ? `<ul>${s.cheats.map(c => `<li>${esc(c)}</li>`).join("")}</ul>` : `<p>No named cheats found in this file.</p>`}</div>`).join("")}</div>`).join("");
  await modal(`<div class="cheatview"><h3>Cheats and mods for ${esc(title)}</h3><p class="mono">${esc(id)}</p>
    ${body || "<p>Nothing is installed for this game.</p>"}
    <p class="hint2">Each build is one version of the game; the emulator uses the file that matches the version you run. Switch cheats on or off on the PS5: Triangle on the game, then Mods.</p></div>`, [{ label: "Close", cls: "primary" }]);
}
const FWD_OK = []; // home screen tiles are switched off for now; put "PPSA99008" back to bring them back
let FWD = { template: false, items: [] };
async function fwdLoad() { try { FWD = await api("fwd/state", {}); renderEmu(); } catch (e) { /* tiles just show no home screen state */ } }
function fwdFor(app, name) { return app && FWD_OK.includes(app.folder) ? (FWD.items || []).find(x => x.target === app.folder && x.rom === name) : null; }
function emuIcon(a) { return a.icon ? `<img alt="" src="/cover?k=${encodeURIComponent("id:" + a.icon)}">` : `<span class="ph">${esc((a.name || "?").slice(0, 2).toUpperCase())}</span>`; }
function renderEmu() {
  const bar = $("#emuBar"), body = $("#emuBody");
  const app = EM.cur && EM.apps.find(a => a.path === EM.cur);
  let bh, html;
  if (!app) {
    bh = `<span class="ps5stat">${EM.busy ? "Looking for emulators on the PS5…" : EM.loaded ? `${EM.apps.length} installed` : ""}</span><span class="grow"></span>
      <button class="btn primary" data-emu="zip">Install from a ZIP</button><button class="btn ghost" data-emu="browse">Browse files</button><button class="btn ghost" data-emu="refresh">Refresh</button>`;
    if (!S.settings.PS5IP) html = `<div class="empty"><p>Add your PS5's IP address in Settings to manage emulators.</p><button class="btn primary" data-act="gosettings">Open Settings</button></div>`;
    else if (EM.err) html = `<div class="empty"><p>${esc(EM.err)}</p></div>`;
    else if (!EM.apps.length) html = `<div class="empty"><p>${EM.busy || !EM.loaded ? "Looking for emulators on the PS5…" : "No emulators on the PS5 yet. Click Install from a ZIP and pick an emulator's download."}</p></div>`;
    else html = `<div class="emugrid">` + EM.apps.map(a => `<button class="emucard" data-emuopen="${esc(a.path)}">
        ${emuIcon(a)}<span class="emutxt"><b>${esc(a.name)}</b><small>${esc(a.system || "Homebrew app")}</small>
        <small>${a.gamesDir ? `${a.games} game${a.games === 1 ? "" : "s"}${a.size ? ", " + size(a.size) : ""}` : "Games folder not set"} · ${esc(a.drive)}</small></span></button>`).join("") + `</div>`;
  } else {
    bh = `<button class="btn ghost" data-emu="back">‹ All emulators</button><span class="grow"></span>
      <button class="btn primary" data-emu="add" ${emuSlot(app) ? "" : "disabled"}>${esc((emuSlot(app) || {}).add || "Add game")}</button><button class="btn ghost" data-emu="addfolder" ${emuSlot(app) ? "" : "disabled"}>Add a folder</button>
      ${app.folder === "PPSA99008" && emuSlot(app) && emuSlot(app).key === "games" ? `<button class="btn ghost" data-emu="ids" title="Put each game's ID in its file name, so cheats and mods match up">Add game IDs</button>` : ""}<button class="btn ghost" data-emu="update" title="Install a newer version from its ZIP. Your games and settings stay">Update emulator</button><button class="btn ghost" data-emu="files">Browse files</button><button class="btn ghost" data-emu="refresh">Refresh</button>`;
    const head = `<div class="emuhead">${emuIcon(app)}<div><h2>${esc(app.name)}</h2><p>${esc(app.system || "Homebrew app")} · ${esc(app.drive)} · <span class="mono">${esc(app.path)}</span></p>
      <p>${app.gamesDir ? `Games go in <span class="mono">${esc(app.gamesDir)}</span>` : "Loadout doesn't know where this app keeps its games."} ${app.gamesDir && !app.gamesDir.startsWith(app.path + "/") ? "" : `<button class="linkbtn" data-emu="setdir">${app.gamesDir ? "Change" : "Choose the games folder"}</button> · `}<button class="linkbtn dangerlink" data-emu="remove">Remove this emulator</button></p></div></div>`;
    const sl = emuSlot(app), dir = sl ? sl.dir : "", isGames = !sl || sl.key === "games";
    const tabs = (app.slots || []).length > 1 ? `<div class="filters emutabs">` + app.slots.map(s => `<button data-emuslot="${esc(s.key)}" aria-pressed="${sl && s.key === sl.key}">${esc(s.label)}<i>${s.count}</i></button>`).join("") + `</div>` : "";
    const pending = S.build.jobs.filter(x => x.kind === "fxup" && x.key.startsWith("fx:" + dir + "/") && ["queued", "copying"].includes(x.state) && !EM.games.some(g => "fx:" + dir + "/" + g.name === x.key));
    const rows = pending.map(jb => `<div class="row fx"><span class="fxname"><b>${esc(jb.out)}</b></span><span class="size">${esc(size(jb.size))}</span><span class="rowprog"><span class="bar sending"><i style="width:${jb.pct / 10}%"></i></span>${jb.state === "queued" ? "Waiting" : "Copying " + Math.floor(jb.pct / 10) + "%"}</span></div>`)
      .concat([...EM.games].sort((a, b) => a.name.localeCompare(b.name)).map(g => {
        const p = dir + "/" + g.name, jb = jobFor("fx:" + p), live = jb && ["queued", "copying"].includes(jb.state);
        return `<div class="row fx${isGames ? " emugame" : ""}"><span class="fxname">${isGames ? (g.cover ? `<img class="emuthumb" alt="" src="/cover?k=${encodeURIComponent("emu:" + g.cover)}">` : `<span class="emuthumb ph"></span>`) : g.isDir ? ICON.folder : ""}<span class="gtxt2"><b>${esc(g.title || g.name)}</b>${g.title ? `<small>${esc(g.name)}</small>` : ""}</span>${isGames ? cheatTag(g) : ""}</span><span class="size">${g.isDir ? "Folder" : esc(size(g.size))}</span>
          ${live ? `<span class="rowprog"><span class="bar sending"><i style="width:${jb.pct / 10}%"></i></span>Copying ${Math.floor(jb.pct / 10)}%</span>` : `<span class="rowbtns">${isGames ? `<button class="btn ghost sm" data-emuinfo="${esc(p)}">Details</button>` : ""}<button class="btn ghost sm" data-emuren="${esc(p)}">Rename</button><button class="btn ghost sm danger" data-emudel="${esc(p)}" data-isdir="${g.isDir ? 1 : ""}">${ICON.trash}Remove</button></span>`}</div>`;
      }));
    if (EM.groups) {
      rows.length = 0;
      for (const g of EM.groups) {
        const open = EM.open.has(g.name);
        rows.push(`<div class="row fx grp"><span class="fxname"><button class="grptog" data-emugrp="${esc(g.name)}" aria-expanded="${open}" title="${open ? "Hide" : "Show"} the files">${open ? "▾" : "▸"}</button><b>${esc(g.name)}</b></span>
          <span class="size">${g.files.length} file${g.files.length === 1 ? "" : "s"}, ${esc(size(g.size))}</span>
          <span class="rowbtns">${g.known ? `<small class="grpwhen">${esc(when(g.when))}</small>` : ""}<button class="btn ghost sm danger" data-emugrpdel="${esc(g.name)}">${ICON.trash}Remove</button></span></div>`);
        if (open) for (const fl of g.files) rows.push(`<div class="row fx grpfile"><span class="fxname">${esc(fl.name)}</span><span class="size">${esc(size(fl.size))}</span><span></span></div>`);
      }
    }
    const sub = sl && !isGames ? `<p class="emuhint">${esc(sl.hint.charAt(0).toUpperCase() + sl.hint.slice(1))}. These go in <span class="mono">${esc(sl.dir)}</span></p>` : "";
    const tilesOn = isGames && !EM.groups && emuView === "tiles";
    const toggle = isGames && !EM.groups ? `<div class="seg emuseg"><button data-emuview="tiles" title="Tiles" aria-pressed="${emuView === "tiles"}">${GRID_SVG}</button><button data-emuview="list" title="List" aria-pressed="${emuView === "list"}">${LIST_SVG}</button></div>` : "";
    if (tilesOn) {
      const tiles = pending.map(jb => `<article class="tile"><div class="cover"><span class="initials">${esc(initials(jb.out))}</span></div>
          <div class="meta"><h4>${esc(jb.out)}</h4><div class="sub"><span>${jb.state === "queued" ? "Waiting" : "Copying " + Math.floor(jb.pct / 10) + "%"}</span></div></div>
          <div class="mini sending"><i style="width:${jb.pct / 10}%"></i></div><div class="actions"></div></article>`)
        .concat([...EM.games].sort((a, b) => (a.title || a.name).localeCompare(b.title || b.name)).map(g => {
          const p = dir + "/" + g.name, jb = jobFor("fx:" + p), live = jb && ["queued", "copying"].includes(jb.state);
          return `<article class="tile"><div class="cover" data-emuinfo="${esc(p)}" style="cursor:pointer" title="Show details">${g.cover ? `<img alt="" loading="lazy" src="/cover?k=${encodeURIComponent("emu:" + g.cover)}">` : `<span class="initials">${esc(initials(g.title || g.name))}</span>`}</div>
            <div class="meta"><h4 title="${esc(g.name)}">${esc(g.title || g.name.replace(/\.[^.]+$/, "").replace(/\s*[\[(][^\])]*[\])]/g, "").trim() || g.name)}</h4><div class="sub"><span>${g.isDir ? "Folder" : esc(size(g.size))}</span>${fwdFor(app, g.name) ? `<span class="tag sky" title="This game has a tile on the PS5's home screen">Home screen</span>` : ""}${cheatTag(g)}<span>${esc((g.name.match(/\.([^.]+)$/) || [])[1] || "").toUpperCase()}</span></div></div>
            ${live ? `<div class="mini sending"><i style="width:${jb.pct / 10}%"></i></div><div class="actions"><span class="sub">Copying ${Math.floor(jb.pct / 10)}%</span></div>` : `<div class="actions"><button class="btn ghost wide" data-emuinfo="${esc(p)}" title="File name, location and size">Details</button><button class="btn ghost wide" data-emuren="${esc(p)}" title="Give the file a proper name">Rename</button><button class="btn ghost icon danger" data-emudel="${esc(p)}" data-isdir="${g.isDir ? 1 : ""}" title="Remove from the PS5">${ICON.trash}</button></div>`}</article>`;
        }));
      if (tiles.length) { rows.length = 0; rows.push(`<div class="grid emutiles">${tiles.join("")}</div>`); }
    }
    html = head + `<div class="emutabrow">${tabs}${toggle}</div>` + `<h3 class="svhead">${esc(sl ? sl.label : "Games")}</h3>` + sub + (rows.length ? rows.join("") : `<div class="empty small"><p>${!sl ? "Choose the games folder first." : EM.gbusy ? "Reading the folder…" : isGames ? "No games yet. Click Add game and pick a game file from your PC." : `Nothing here yet. Click ${esc(sl.add)} and pick the files from your PC.`}</p></div>`);
  }
  if (bar.dataset.sig !== bh) { bar.dataset.sig = bh; bar.innerHTML = bh; }
  if (body.dataset.sig !== html) { body.dataset.sig = html; body.innerHTML = html; }
}
async function emuPickDir(app) {
  let d; try { d = await api("fx/list", { Dir: app.path, Mode: "homebrew" }); } catch (e) { toast(e.message, true); return; }
  const dirs = (d.entries || []).filter(e => e.isDir);
  const r = await modal(`<h3>Where do games go?</h3><p>Pick the folder inside ${esc(app.name)} that its instructions name for games.</p><ul class="ftplist">` +
    dirs.map(e => `<li><button data-pick="${esc(app.path + "/" + e.name)}">${ICON.folder}${esc(e.name)}</button></li>`).join("") +
    `<li><button data-pick="${esc(app.path)}">${ICON.folder}<span>(the app's own folder)</span></button></li></ul>`, [{ label: "Cancel" }]);
  if (r && r.pick) { await act("emu/setdir", { Folder: app.folder, AppPath: app.path, Dir: r.pick }); emuLoad(); }
}
$("#view-emu").addEventListener("click", async e => {
  const t = e.target.closest("button, [data-emuinfo]"); if (!t) return;
  const app = EM.cur && EM.apps.find(a => a.path === EM.cur);
  if (t.dataset.emuopen) { EM.cur = t.dataset.emuopen; EM.slot = "games"; EM.groups = null; EM.games = []; renderEmu(); emuGames(); return; }
  if (t.dataset.emuslot) { EM.slot = t.dataset.emuslot; EM.games = []; EM.groups = null; renderEmu(); emuGames(); return; }
  if (t.dataset.emuview) { emuView = t.dataset.emuview; try { localStorage.setItem("emuView", emuView); } catch (e) { /* not saved; still switches */ } renderEmu(); return; }
  if (t.dataset.emugrp) { EM.open.has(t.dataset.emugrp) ? EM.open.delete(t.dataset.emugrp) : EM.open.add(t.dataset.emugrp); renderEmu(); return; }
  if (t.dataset.emugrpdel) {
    const g = (EM.groups || []).find(x => x.name === t.dataset.emugrpdel); if (!g) return;
    if (await confirmBox("Remove this from the PS5?", `<b>${esc(g.name)}</b> (${g.files.length} files) will be deleted from the PS5. The copy on your PC isn't touched.`, "Remove", "danger solid")) { await act("emu/removegroup", { Dir: emuSlot(app).dir, Name: g.name }); emuGames(); emuLoad(); }
    return;
  }
  if (t.dataset.emucheats) { return cheatsShow(EM.apps.find(a => a.path === EM.cur), t.dataset.emucheats, t.dataset.title || ""); }
  if (t.dataset.emuinfo) {
    const p = t.dataset.emuinfo, name = p.split("/").pop(), g = (EM.games || []).find(x => x.name === name) || { name };
    const app = EM.apps.find(a => a.path === EM.cur), canFwd = app && FWD_OK.includes(app.folder) && !g.isDir, fw = fwdFor(app, name);
    const ext = ((name.match(/\.([^.]+)$/) || [])[1] || "").toUpperCase(), title = g.title || name.replace(/\.[^.]+$/, "");
    const line = (label, val, copy) => `<div class="inforow"><span>${label}</span><div><b class="mono">${esc(val)}</b>${copy ? `<button class="btn ghost sm" data-copytxt="${esc(val)}">Copy</button>` : ""}</div></div>`;
    const plain = (label, val) => `<div class="inforow"><span>${label}</span><div><b>${val}</b></div></div>`;
    const dir = p.slice(0, p.length - name.length - 1);
    const isEden = app && app.folder === "PPSA99008" && !g.isDir && /\/roms$/.test(dir);
    const gid = (name.match(/[\[(]([0-9A-Fa-f]{16})[\])]/) || [])[1] || "";
    const btns = [{ label: "Close" }];
    if (isEden && !gid) btns.push({ label: "Add game ID to the name", cls: "primary" });
    if (isEden && gid && (EM.cheats || {})[gid.toUpperCase()]) btns.push({ label: "See cheats", cls: "primary" });
    if (canFwd) btns.push(fw ? { label: "Remove from home screen", cls: "ghost danger" } : { label: "Show on home screen", cls: "primary" });
    const r = await modal(`<div class="emuinfo"><div class="emuinfohead">${g.cover ? `<img alt="" src="/cover?k=${encodeURIComponent("emu:" + g.cover)}">` : ""}<h3>${esc(title)}</h3></div>
      ${line("ROM name", name, true)}${line("Where it is on the PS5", p, true)}
      ${plain("Size", g.isDir ? "Folder" : esc(size(g.size)))}${ext && !g.isDir ? plain("Type", esc(ext)) : ""}
      ${plain("Name shown in the emulator", g.title ? esc(g.title) : "Not known yet (the emulator hasn't saved one)")}
      ${plain("Cover", g.cover ? "Saved by the emulator" : "None yet")}
      ${isEden ? (gid ? line("Game ID", gid.toUpperCase(), true) : plain("Game ID", "Not in the file name yet. Cheats and mods are matched by it")) : ""}
      ${isEden && gid ? plain("Cheats and mods", (EM.cheats || {})[gid.toUpperCase()] ? `${EM.cheats[gid.toUpperCase()]} file(s) installed` : "None installed") : ""}
      ${canFwd ? plain("PS5 home screen", fw ? `Has a tile (${esc(fw.id)})` : "No tile") : ""}</div>`, btns);
    if (r && r.i >= 1 && btns[r.i].label === "Add game ID to the name") { await gameIdAdd(dir, name, title); return; }
    if (r && r.i >= 1 && btns[r.i].label === "See cheats") { await cheatsShow(app, gid.toUpperCase(), title); return; }
    if (r && r.i >= 1 && canFwd) { if (fw) await fwdRemove(fw, title); else await fwdMake(app, g, title); }
    return;
  }
  if (t.dataset.emuren) {
    const old = t.dataset.emuren.split("/").pop(), ext = (old.match(/\.[^.]+$/) || [""])[0], stem = old.slice(0, old.length - ext.length);
    const r = await modal(`<h3>Rename this game</h3><p>Type the game's name. The file on the PS5 is renamed, so the emulator shows the same name. The file type (${esc(ext || "none")}) is kept.</p><input value="${esc(stem)}" spellcheck="false">`, [{ label: "Cancel" }, { label: "Rename", cls: "primary" }]);
    const name = r && r.i === 1 ? r.value.trim().replace(/[\\/:*?"<>|]/g, "").replace(/\.+$/, "") : "";
    if (name && name !== stem) { await act("fx/rename", { Path: t.dataset.emuren, Name: name + ext }); emuGames(); }
    return;
  }
  if (t.dataset.emudel) {
    const name = t.dataset.emudel.split("/").pop(), eg = (EM.games || []).find(x => x.name === name), shown = eg && eg.title && eg.isDir ? `${eg.title} (${name})` : name;
    if (await confirmBox("Remove this from the PS5?", `<b>${esc(shown)}</b> will be deleted from the PS5. The copy on your PC isn't touched.`, "Remove", "danger solid")) { await act("fx/delete", { Path: t.dataset.emudel, IsDir: !!t.dataset.isdir }); emuGames(); emuLoad(); }
    return;
  }
  switch (t.dataset.emu) {
    case "zip": { const r = (await api("fx/list", { Mode: "homebrew" }).catch(() => ({}))).roots || []; if (r[0]) await act("fx/installzip", { Dir: r[0].path }); return; }
    case "browse": FX.dirs.homebrew = ""; return showView("homebrew");
    case "files": FX.dirs.homebrew = app.path; return showView("homebrew");
    case "refresh": return emuLoad();
    case "back": EM.cur = ""; return renderEmu();
    case "add":
      if (emuSlot(app).key === "mods") return cheatsAdd(emuSlot(app).dir);
      return act("emu/add", { Dir: emuSlot(app).dir, Unzip: !!emuSlot(app).unzip });
    case "addfolder": return act("emu/add", { Dir: emuSlot(app).dir, Folder: true });
    case "setdir": return emuPickDir(app);
    case "ids": return gameIdAll(emuSlot(app).dir);
    case "update":
      if (await confirmBox("Update this emulator?", `Pick the ZIP of the newer <b>${esc(app.name)}</b>. Loadout copies it over <span class="mono">${esc(app.path)}</span>, replacing the app's own files. Your games, keys, firmware and settings are kept. If the ZIP is for a different app, nothing is changed.<br><br>Close the emulator on the PS5 first.`, "Pick the ZIP")) await act("emu/update", { Path: app.path, Name: app.name });
      return;
    case "remove":
      if (await confirmBox("Remove this emulator?", `<b>${esc(app.name)}</b> and everything in <span class="mono">${esc(app.path)}</span> will be deleted from the PS5. This can't be undone.${(app.slots || []).some(s => !s.dir.startsWith(app.path + "/")) ? " Its games and other files are kept in a separate folder and are not deleted." : ""}`, "Remove", "danger solid")) { await act("fx/delete", { Path: app.path, IsDir: true }); EM.cur = ""; emuLoad(); }
      return;
  }
});
let emuJobsDone = 0;

// ---------------- PS5 files ----------------
let FX = { mode: "games", dirs: {}, dir: "", roots: [], entries: [], busy: false, err: "", missing: false };
async function fxLoad(dir) {
  if (dir !== undefined) FX.dir = dir;
  FX.busy = true; FX.err = ""; renderFiles();
  try {
    const d = await api("fx/list", { Dir: FX.dir, Mode: FX.mode });
    FX.roots = d.roots || []; FX.entries = d.entries || []; FX.missing = !!d.missing; FX.dir = d.dir || "";
    if (!FX.dir && FX.roots.length) { FX.busy = false; return fxLoad(FX.roots[0].path); }
  } catch (e) { FX.err = e.message; FX.entries = []; }
  FX.busy = false; renderFiles();
}
function fxRoot() { return FX.roots.filter(r => FX.dir === r.path || FX.dir.startsWith(r.path + "/")).sort((a, b) => b.path.length - a.path.length)[0]; }
function renderFiles() {
  const root = fxRoot();
  FX.dirs[FX.mode] = FX.dir;
  const hb = FX.mode === "homebrew", fh = $("#fxHelp");
  fh.hidden = !hb; $("#fxZip").hidden = !hb; $("#fxZip").disabled = !root || !!FX.err;
  if (hb) fh.innerHTML = `<b>Emulators and other homebrew apps live here.</b> Each app is one folder. To install one, click <b>Install from a ZIP</b> and pick the ZIP you downloaded; Loadout unzips it and copies the app over. (If you already unzipped it, use <b>Copy a folder here</b>.) To add files to an app, open its folder and the subfolder its instructions name, then copy them in. Apps are started from the homebrew launcher on the PS5.`;
  $("#fxRoots").innerHTML = FX.roots.map(r => `<button data-fxroot="${esc(r.path)}" aria-pressed="${root && root.path === r.path}" title="${esc(r.path)}">${esc(r.label)}</button>`).join("");
  let crumbs = "";
  if (root) {
    const rest = FX.dir.slice(root.path.length).split("/").filter(Boolean);
    let p = root.path;
    crumbs = `<button class="linkbtn" data-fxdir="${esc(p)}">${esc(root.path)}</button>` + rest.map(s => { p += "/" + s; return ` <span>/</span> <button class="linkbtn" data-fxdir="${esc(p)}">${esc(s)}</button>`; }).join("");
  }
  $("#fxPath").innerHTML = crumbs;
  $("#fxMkdir").disabled = $("#fxUpload").disabled = $("#fxUploadDir").disabled = !FX.dir || !!FX.err || (FX.missing && !hb);
  const ents = [...FX.entries].sort((a, b) => (b.isDir - a.isDir) || a.name.localeCompare(b.name));
  let html;
  if (!S.settings.PS5IP) html = `<div class="empty"><p>Add your PS5's IP address in Settings to browse its game folders.</p><button class="btn primary" data-act="gosettings">Open Settings</button></div>`;
  else if (FX.busy && !ents.length) html = `<div class="empty"><p>Reading the folder…</p></div>`;
  else if (FX.err) html = `<div class="empty"><p>${esc(FX.err)}</p></div>`;
  else if (FX.missing) html = `<div class="empty"><p>${hb ? "There's no homebrew folder here yet. Copy an app's folder here and Loadout creates it." : "This folder isn't on the PS5. The drive may not be plugged in, or nothing has been sent to it yet."}</p></div>`;
  else if (!ents.length) html = `<div class="empty"><p>This folder is empty.</p></div>`;
  else html = ents.map(e => {
    const p = FX.dir + "/" + e.name, jb = jobFor("fx:" + p), live = jb && ["queued", "copying"].includes(jb.state);
    return `<div class="row fx">
      <span class="fxname">${e.isDir ? ICON.folder || "" : ""}${e.isDir ? `<button class="linkbtn" data-fxdir="${esc(p)}">${esc(e.name)}</button>` : `<b>${esc(e.name)}</b>`}</span>
      <span class="size">${e.isDir ? "Folder" : esc(size(e.size))}</span>
      ${live ? `<span class="rowprog"><span class="bar sending"><i style="width:${jb.pct / 10}%"></i></span>${jb.state === "queued" ? "Waiting" : "Copying " + Math.floor(jb.pct / 10) + "%"}</span>`
        : `<span class="rowbtns">${e.isDir ? "" : `<button class="btn ghost sm" data-fxget="${esc(p)}" data-size="${e.size}">Copy to PC</button>`}<button class="btn ghost sm" data-fxren="${esc(p)}">Rename</button><button class="btn ghost sm danger" data-fxdel="${esc(p)}" data-isdir="${e.isDir ? 1 : ""}" title="Delete from the PS5">${ICON.trash}</button></span>`}
    </div>`;
  }).join("");
  if ($("#fxList").dataset.sig !== html) { $("#fxList").dataset.sig = html; $("#fxList").innerHTML = html; }
}
$("#fxRefresh").onclick = () => fxLoad();
$("#fxRoots").onclick = e => { const b = e.target.closest("[data-fxroot]"); if (b) fxLoad(b.dataset.fxroot); };
$("#fxPath").onclick = e => { const b = e.target.closest("[data-fxdir]"); if (b) fxLoad(b.dataset.fxdir); };
$("#fxMkdir").onclick = async () => {
  const r = await modal(`<h3>New folder</h3><p>In ${esc(FX.dir)}</p><input placeholder="Folder name" spellcheck="false">`, [{ label: "Cancel" }, { label: "Create", cls: "primary" }]);
  if (r && r.i === 1 && r.value.trim()) { await act("fx/mkdir", { Dir: FX.dir, Name: r.value }); fxLoad(); }
};
$("#fxUpload").onclick = async () => { await act("fx/upload", { Dir: FX.dir }); };
$("#fxZip").onclick = async () => { const r = fxRoot(); if (r) await act("fx/installzip", { Dir: r.path }); };
$("#fxUploadDir").onclick = async () => { await act("fx/uploaddir", { Dir: FX.dir }); };
$("#fxList").addEventListener("click", async e => {
  const t = e.target.closest("button"); if (!t) return;
  if (t.dataset.fxdir) return fxLoad(t.dataset.fxdir);
  if (t.dataset.fxget) return act("fx/download", { Path: t.dataset.fxget, Size: +t.dataset.size || 0 });
  if (t.dataset.fxren) {
    const old = t.dataset.fxren.split("/").pop();
    const r = await modal(`<h3>Rename</h3><input value="${esc(old)}" spellcheck="false">`, [{ label: "Cancel" }, { label: "Rename", cls: "primary" }]);
    if (r && r.i === 1 && r.value.trim() && r.value !== old) { await act("fx/rename", { Path: t.dataset.fxren, Name: r.value }); fxLoad(); }
    return;
  }
  if (t.dataset.fxdel) {
    const name = t.dataset.fxdel.split("/").pop(), dir = !!t.dataset.isdir;
    if (await confirmBox(dir ? "Delete this folder from the PS5?" : "Delete this file from the PS5?", `<b>${esc(name)}</b>${dir ? " and everything inside it" : ""} will be deleted from the PS5. This can't be undone.`, "Delete", "danger solid")) { await act("fx/delete", { Path: t.dataset.fxdel, IsDir: dir }); fxLoad(); }
  }
});
let fxJobsDone = 0;

// ---------------- saves ----------------
let lastSavePeek = 0, svGame = "";
// saveLink is the "View saves" link on a Library tile, for games with a save on the PS5 or a backup on this PC.
function saveLink(it) {
  const sv = S.saves; if (!sv || !it.titleId || it.isDlc) return "";
  const live = sv.saves.filter(s => s.titleId === it.titleId).length, bk = sv.backups.filter(b => b.titleId === it.titleId && !b.auto).length;
  if (!live && !bk) return "";
  return `<button class="dlclink savelink" data-act="saves" data-id="${esc(it.titleId)}" title="${live ? "Save on the PS5" : "No save on the PS5"}, ${bk} backup${bk === 1 ? "" : "s"} on this PC">View saves${bk ? ` · ${bk} backup${bk === 1 ? "" : "s"}` : live ? " · not backed up" : ""}</button>`;
}
function saveName(id, fallback) {
  const it = S.items.find(i => i.titleId === id && !i.isDlc);
  return (it && it.name) || fallback || ((S.ps5 && S.ps5.games && (S.ps5.games[id] || [])[0]) || {}).name || id;
}
function saveCover(id) {
  const it = S.items.find(i => i.titleId === id && !i.isDlc && i.cover);
  return it ? `<img alt="" src="/cover?k=${encodeURIComponent(it.coverKey || it.libKey)}">` : ((S.ps5 || {}).icons || {})[id] ? `<img alt="" src="/cover?k=${encodeURIComponent("id:" + id)}">` : `<span class="ph"></span>`;
}
const when = t => { const d = new Date(t); return d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) + ", " + d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }); };
function renderSaves() {
  const all = S.saves || { saves: [], backups: [] };
  const sv = svGame ? { ...all, saves: all.saves.filter(s => s.titleId === svGame), backups: all.backups.filter(b => b.titleId === svGame) } : all;
  $("#nav-saves").textContent = all.saves.length || "";
  $("#svStatus").innerHTML = !S.settings.PS5IP ? "" : sv.busy ? "Reading saves from the PS5…" : sv.err ? `<span style="color:var(--coral)">${esc(sv.err)}</span>` : sv.scanned && !sv.scanned.startsWith("0001") ? `Checked ${ago(sv.scanned)}` : "";
  $("#svRescan").disabled = !S.settings.PS5IP || sv.busy;
  $("#svAll").disabled = !sv.saves.length;
  $("#svAll").textContent = svGame ? "Back up this game's save" : "Back up every save";
  $("#svSummary").textContent = `${sv.saves.length} save${sv.saves.length === 1 ? "" : "s"} on the PS5, ${sv.backups.length} backup${sv.backups.length === 1 ? "" : "s"} on this PC`;
  const manyUsers = new Set(sv.saves.map(s => s.user)).size > 1 || new Set(sv.backups.map(b => b.user)).size > 1;
  const who = x => x.userName || x.user;
  const prog = (jb, word) => `<span class="rowprog"><span class="bar sending"><i style="width:${jb.pct / 10}%"></i></span>${jb.state === "queued" ? "Waiting" : jb.note ? esc(jb.note) : word + " " + Math.floor(jb.pct / 10) + "%"}</span>`;
  let top;
  if (!S.settings.PS5IP) top = `<div class="empty"><p>Add your PS5's IP address in Settings to see its game saves.</p><button class="btn primary" data-act="gosettings">Open Settings</button></div>`;
  else if (!sv.saves.length) top = `<div class="empty"><p>${sv.busy ? "Reading saves from the PS5…" : sv.err ? "Loadout couldn't read the saves. Make sure the PS5 is on and its FTP server is running, then click Rescan saves." : "No game saves found on the PS5."}</p></div>`;
  else top = sv.saves.map(s => {
    const jb = jobFor("save:" + s.path), live = jb && ["queued", "saving", "deleting"].includes(jb.state);
    const last = sv.backups.find(b => b.titleId === s.titleId && b.user === s.user && !b.auto);
    return `<div class="row sv">
      <span class="gname">${saveCover(s.titleId)}<span class="gtxt"><b>${esc(saveName(s.titleId))}</b><small>${esc(s.titleId)} · ${s.kind === "ps4" ? "PS4 game" : "PS5 game"}${manyUsers ? " · " + esc(who(s)) : ""}</small></span></span>
      <span class="size">${esc(size(s.size))}</span>
      <span class="svwhen">${last ? "Backed up " + esc(when(last.when)) : `<em>Not backed up</em>`}</span>
      ${live ? prog(jb, jb.kind === "savedel" ? "Deleting" : "Backing up") : `<span class="rowbtns"><button class="btn ghost sm" data-svbak="${esc(s.path)}">Back up</button><button class="btn ghost sm danger" data-svdel="${esc(s.path)}" title="Delete this save from the PS5">${ICON.trash}</button></span>`}
    </div>`;
  }).join("");
  const bk = sv.backups.map(b => {
    const jb = jobFor("rest:" + b.id), live = jb && ["queued", "restoring"].includes(jb.state);
    return `<div class="row sv">
      <span class="gname">${saveCover(b.titleId)}<span class="gtxt"><b>${esc(saveName(b.titleId, b.name))}</b><small>${esc(b.titleId)}${manyUsers ? " · " + esc(who(b)) : ""}${b.auto ? ` · <span class="svauto">safety copy from before a ${b.why === "delete" ? "delete" : "restore"}</span>` : ""}</small></span></span>
      <span class="size">${esc(size(b.size))}</span>
      <span class="svwhen">${esc(when(b.when))}</span>
      ${live ? prog(jb, "Restoring") : `<span class="rowbtns"><button class="btn ghost sm" data-svrest="${esc(b.id)}">Restore to PS5</button><button class="btn ghost sm danger" data-svrm="${esc(b.id)}" title="Move this backup to the Recycle Bin">${ICON.trash}</button></span>`}
    </div>`;
  }).join("");
  const html = (svGame ? `<div class="svfilter">Showing saves for <b>${esc(saveName(svGame))}</b><button class="btn ghost sm" data-svall="1">Show all games</button></div>` : "") + `<h3 class="svhead">Saves on the PS5</h3>${top}<h3 class="svhead">Backups on this PC</h3>${bk || `<div class="empty small"><p>No backups yet. Back up a save and it shows up here, ready to restore.</p></div>`}`;
  if ($("#svList").dataset.sig !== html) { $("#svList").dataset.sig = html; $("#svList").innerHTML = html; }
}
$("#svRescan").onclick = () => act("saves/scan", {});
$("#svOpen").onclick = () => act("open", { path: (S.saves || {}).dir });
const saveItem = s => ({ User: s.user, TitleID: s.titleId, Kind: s.kind, Name: saveName(s.titleId) });
$("#svAll").onclick = () => act("saves/backup", { Items: S.saves.saves.filter(s => !svGame || s.titleId === svGame).map(saveItem) });
$("#svList").addEventListener("click", async e => {
  const t = e.target.closest("button"); if (!t) return;
  if (t.dataset.svall) { svGame = ""; renderSaves(); return; }
  if (t.dataset.svbak) { const s = S.saves.saves.find(x => x.path === t.dataset.svbak); if (s) act("saves/backup", { Items: [saveItem(s)] }); return; }
  if (t.dataset.svdel) {
    const s = S.saves.saves.find(x => x.path === t.dataset.svdel); if (!s) return;
    if (await confirmBox("Delete this save from the PS5?", `This removes the save for ${esc(saveName(s.titleId))} from the PS5. Close the game first. Loadout copies the save to this PC before deleting it, so you can put it back with Restore.`, "Delete from PS5", "danger solid"))
      act("saves/delete", { Items: [saveItem(s)] });
    return;
  }
  if (t.dataset.svrest) {
    const b = S.saves.backups.find(x => x.id === t.dataset.svrest); if (!b) return;
    if (await confirmBox("Restore this save?", `This replaces the save for ${esc(saveName(b.titleId, b.name))} on the PS5 with the one from ${esc(when(b.when))}. Close the game on the PS5 first. Loadout keeps a safety copy of the save that's there now.`, "Restore"))
      act("saves/restore", { ID: b.id });
    return;
  }
  if (t.dataset.svrm) { if (await confirmBox("Remove this backup?", "It goes to the Recycle Bin on this PC. Nothing changes on the PS5.", "Remove", "danger solid")) act("saves/remove", { ID: t.dataset.svrm }); }
});

// ---------------- on PS5 ----------------
let psel = new Set(), psDrive = "";
const KIND = { image: "exFAT image", folder: "Game folder", installed: "Installed" };
function ps5Rows() {
  const rows = [];
  for (const [id, copies] of Object.entries((S.ps5 && S.ps5.games) || {})) {
    const it = S.items.find(i => i.titleId === id && !i.isDlc && (i.present || i.exfatOnly)) || S.items.find(i => i.titleId === id && !i.isDlc);
    for (const c of copies) {
      const base = c.path.split("/").pop().replace(/\.(exfat|ffpkg|ffpfsc)$/i, "");
      rows.push({ ...c, id, item: it, name: (it && it.name) || c.name || base.replace(/\s*[\[(]?[A-Z]{4}\d{5}.*$/, "") || id });
    }
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name) || a.drive.localeCompare(b.drive));
}
function ago(t) {
  const s = Math.max(0, Math.round((Date.now() - Date.parse(t)) / 1000));
  return s < 10 ? "just now" : s < 60 ? `${s}s ago` : `${Math.round(s / 60)} min ago`;
}
function renderPS5() {
  const p = S.ps5 || {}, all = ps5Rows();
  $("#nav-ps5").textContent = all.length || "";
  const ds = drives();
  if (psDrive && !ds.some(d => d.short === psDrive)) psDrive = "";
  $("#ps5Filter").innerHTML = ds.length ? [["", "All", all.length], ...ds.map(d => [d.short, d.short, all.filter(r => r.drive === d.short).length, d])].map(([v, l, n, d]) =>
    `<button data-psdrive="${esc(v)}" aria-pressed="${psDrive === v}" title="${d ? esc(d.label) + (d.free >= 0 ? ", " + size(d.free) + " free" : "") : ""}">${esc(l)}<i>${n}</i></button>`).join("") : "";
  $("#ps5Status").innerHTML = !S.settings.PS5IP ? "" : p.busy ? "Checking the PS5…" : !p.online ? `<span style="color:var(--coral)">PS5 not reachable</span>` : `Checked ${ago(p.checked)}`;
  $("#ps5Rescan").disabled = !S.settings.PS5IP || p.busy;
  const rows = all.filter(r => !psDrive || r.drive === psDrive);
  for (const k of [...psel]) if (!all.some(r => r.path === k && r.kind !== "installed")) psel.delete(k);
  const able = rows.filter(r => r.kind !== "installed");
  const pa = $("#ps5All");
  pa.checked = able.length > 0 && able.every(r => psel.has(r.path)); pa.indeterminate = !pa.checked && able.some(r => psel.has(r.path));
  pa.disabled = !able.length;
  const selRows = all.filter(r => psel.has(r.path));
  const selSize = selRows.reduce((a, r) => a + (r.kind === "image" ? r.size || 0 : 0), 0);
  $("#ps5Summary").textContent = selRows.length ? `${selRows.length} selected` + (selSize ? `, ${size(selSize)}` : "") : all.length ? `${all.length} on the PS5` : "";
  const pd = $("#ps5DelSel");
  if (S.build.ps5Busy) { pd.disabled = false; pd.textContent = "Stop PS5 jobs"; pd.dataset.mode = "stop"; }
  else { pd.disabled = !selRows.length; pd.textContent = selRows.length > 1 ? `Delete ${selRows.length} from PS5` : "Delete selected from PS5"; pd.dataset.mode = ""; }
  const canMove = drives().length > 1;
  const moveSel = selRows.filter(r => r.kind === "image");
  const pm = $("#ps5MoveSel");
  pm.hidden = !canMove;
  pm.disabled = !moveSel.length || S.build.ps5Busy;
  pm.textContent = moveSel.length > 1 ? `Move ${moveSel.length} to another drive` : "Move selected to another drive";
  let empty = "";
  if (!S.settings.PS5IP) empty = `<p>Add your PS5's IP address in Settings to see what's on it.</p><button class="btn primary" data-act="gosettings">Open Settings</button>`;
  else if (!p.online && p.checked && !p.checked.startsWith("0001")) empty = `<p>The PS5 isn't answering, so Loadout can't tell what's on it right now. Make sure it's on and its FTP server is running, then click Rescan PS5.</p>`;
  else if (!p.scanned) empty = `<p>Looking at your PS5…</p>`;
  else if (!rows.length) empty = p.partial ? `<p>The PS5 was too busy to list its games just now (this happens while it's receiving). Click Rescan PS5 in a moment.</p>` : `<p>${psDrive ? "Nothing on this drive." : "No games found on the PS5."}</p>` + ((p.report || []).length ? `<details class="scanrep"><summary>What Loadout saw on the PS5</summary><pre>${esc(p.report.join("\n"))}</pre><button class="btn ghost sm" data-copyrep="1">Copy this</button></details>` : "");
  $("#ps5List").innerHTML = empty ? `<div class="empty">${empty}</div>` : rows.map(r => {
    const it = r.item, inst = r.kind === "installed";
    const mj = jobFor("ps5:" + r.path), mv = mj && mj.kind === "ps5move" && ["queued", "moving"].includes(mj.state) ? mj : null;
    const pc = it ? (it.image ? "Image on your PC" : it.present ? "Folder on your PC" : "") : "Not in your library";
    return `<div class="row ps5">
      <label class="check"><input type="checkbox" data-psel="${esc(r.path)}" ${psel.has(r.path) ? "checked" : ""} ${inst ? "disabled" : ""}><span></span></label>
      <span class="gname">${it && it.cover ? `<img alt="" src="/cover?k=${encodeURIComponent(it.coverKey || it.libKey)}">` : (S.ps5.icons || {})[r.id] ? `<img alt="" src="/cover?k=${encodeURIComponent("id:" + r.id)}">` : `<span class="ph"></span>`}<span class="gtxt"><b>${esc(r.name)}</b><small>${esc(r.id)}${pc ? " · " + esc(pc) : ""} · <span class="mono" title="${esc(r.path)}">${esc(r.path)}</span></small></span></span>
      <span class="drv">${esc(r.drive)}</span>
      <span class="kind">${esc(KIND[r.kind] || r.kind)}</span>
      <span class="size">${r.kind === "image" && r.size ? esc(size(r.size)) : ""}</span>
      ${mv ? `<span class="rowprog"><span class="bar sending"><i style="width:${mv.pct / 10}%"></i></span>${mv.state === "queued" ? "Waiting to move" : "Moving " + Math.floor(mv.pct / 10) + "%"}</span>`
        : inst ? `<span class="instnote" title="Installed games are deleted from the PS5's own game menu">Delete on PS5</span>`
        : `<span class="rowbtns">${r.kind === "image" && canMove ? `<button class="btn ghost sm" data-psmove="${esc(r.path)}" title="Move to another PS5 drive">${ICON.arrow}Move</button>` : ""}<button class="btn ghost sm danger" data-psdel="${esc(r.path)}">${ICON.trash}Delete</button></span>`}
    </div>`;
  }).join("");
}
async function movePS5Rows(list) {
  list = list.filter(r => r.kind === "image");
  if (!list.length) { toast("Only exFAT images can be moved between drives.", true); return; }
  if (!(S.ps5 && S.ps5.spaceFrom === "shsrv")) {
    const r = await modal(`<h3>Moving needs shsrv</h3><p>The move happens on the PS5 itself, using the same shsrv payload that shows free space. It isn't running right now.</p>`, [{ label: "Cancel" }, { label: "Open Payloads", cls: "primary" }]);
    if (r && r.i === 1) { showView("payloads"); }
    return;
  }
  const total = list.reduce((a, r) => a + (r.size || 0), 0);
  const from = new Set(list.map(r => r.drive));
  const title = list.length === 1 ? `Move ${esc(list[0].name)} to which drive?` : `Move ${list.length} games to which drive?`;
  const r = await modal(`<h3>${title}</h3><p>${esc(size(total))} to move. The PS5 copies it over, Loadout checks the copy, then removes the original. Close the game on the PS5 first.</p>
    <ul class="ftplist drivepick">${drives().map(d => {
      const same = from.size === 1 && from.has(d.short), full = d.free >= 0 && total > d.free;
      return `<li><button data-pick="${esc(d.root)}" ${same || full ? "disabled" : ""}>${ICON.folder}<span>${esc(d.short)}<i>${esc(d.label)}, ${esc(d.dir)}</i></span><em>${same ? "it's here now" : full ? "not enough room, " + size(d.free) + " free" : d.free >= 0 ? size(d.free) + " free" : ""}</em></button></li>`;
    }).join("")}</ul>`, [{ label: "Cancel" }]);
  if (!r || !r.pick) return;
  await act("ps5/move", { paths: list.map(x => x.path), drive: r.pick });
  psel.clear();
}
async function deletePS5Rows(list) {
  if (!list.length) return;
  const only = list.filter(r => !r.item || (!r.item.image && !r.item.present));
  const total = list.reduce((a, r) => a + (r.kind === "image" ? r.size || 0 : 0), 0);
  const title = list.length === 1 ? `Delete ${list[0].name} from the PS5?` : `Delete ${list.length} games from the PS5?`;
  const ok = await confirmBox(title,
    `${list.slice(0, 8).map(r => `<b>${esc(r.name)}</b> <span class="mono">${esc(r.drive)}: ${esc(r.path)}</span>`).join("<br>")}${list.length > 8 ? `<br>…and ${list.length - 8} more` : ""}
    <br><br>${total ? `That frees about <b>${esc(size(total))}</b>. ` : ""}It's permanent, the PS5 has no Recycle Bin.
    ${only.length ? `<br><b style="color:var(--coral)">${only.length === list.length && list.length === 1 ? "You don't have this game on your PC, so this is your only copy." : `${only.length} of these aren't on your PC, so the PS5 copy is the only one.`}</b>` : "<br>Your copies on the PC stay, so you can send them again later."}
    <br>Close any game that's running on the PS5 first.`, list.length === 1 ? "Delete from PS5" : `Delete ${list.length} from PS5`, "danger solid");
  if (ok) { await act("ps5/delete", { paths: list.map(r => r.path) }); psel.clear(); }
}

async function stopLane(lane) {
  const msg = {
    build: ["Stop building?", "The image being built right now is deleted. Images that already finished are kept. Sending to the PS5 keeps going."],
    ps5: ["Stop the PS5 jobs?", "Anything half-sent is removed from the PS5, and queued sends and deletes are skipped. Building keeps going."],
    "": ["Stop everything?", "The image being built is deleted and anything half-sent is removed from the PS5. Finished images and uploads are kept."],
  }[lane];
  if (await confirmBox(msg[0], msg[1], "Stop", "danger solid")) act("build/cancel", { lane });
}
// a PS5-lane job's state, whichever field holds it
const ps5State = x => x.kind === "build" ? x.sendState : x.state;
function renderDock() {
  const b = S.build, dock = $("#dock");
  if (!b.running) { dock.hidden = true; return; }
  dock.hidden = false;
  const jobs = b.jobs, lanes = [];
  const bjobs = jobs.filter(x => x.kind === "build" || x.kind === "remove");
  if (bjobs.length) {
    const j = b.building ? (jobs[b.current] || {}) : {};
    const verb = { verifying: "Checking the image for", removing: "Moving to the Recycle Bin:", queued: "Starting" }[j.state] || "Building";
    const n = bjobs.indexOf(j) + 1;
    lanes.push(`<div class="lane"><div class="what" title="${esc(b.file || "")}">${b.building ? `${verb} ${esc(j.name || "")}` : "All images built"}<small>${b.building ? `${n} of ${bjobs.length}${b.speed ? ", " + size(b.speed) + "/s" : ""}${b.eta ? ", " + dur(b.eta) + " left" : ""}` : ""}</small></div>
      <div class="track"><i style="width:${overallPct() / 10}%"></i></div>
      ${b.building ? `<button class="btn danger sm" data-act="stopbuild">Stop</button>` : `<span></span>`}</div>`);
  }
  const pjobs = jobs.filter(x => (x.kind === "build" && x.send) || ["send", "ps5del", "ps5move", "savebak", "saverest", "savedel", "fxup", "fxdown", "fxupdir", "fxzip", "fxzipflat", "pkginst"].includes(x.kind));
  if (pjobs.length) {
    const fin = ["sent", "done", "failed", "cancelled", "skipped"];
    const doneN = pjobs.filter(x => fin.includes(ps5State(x))).length;
    const live = ["sending", "deleting", "moving", "saving", "restoring", "copying"];
    const cur = pjobs.find(x => live.includes(ps5State(x)));
    const liveN = pjobs.filter(x => ps5State(x) === "sending").length;
    const pct = pjobs.reduce((a, x) => a + (fin.includes(ps5State(x)) ? 1000 : live.includes(ps5State(x)) ? (x.kind === "build" ? x.sendPct : x.pct) : 0), 0) / pjobs.length;
    const what = cur ? (ps5State(cur) === "copying" ? `Copying ${esc(cur.name)}` : ps5State(cur) === "saving" ? `Backing up the save for ${esc(cur.name)}` : ps5State(cur) === "restoring" ? `Restoring the save for ${esc(cur.name)}` : ps5State(cur) === "deleting" ? `Deleting ${esc(cur.name)} from the PS5` : ps5State(cur) === "moving" ? `Moving ${esc(cur.name)} (${esc(cur.out)})` : (liveN > 1 ? `Sending ${liveN} games to the PS5` : `Sending ${esc(cur.name)} to the PS5`)) : doneN === pjobs.length ? "PS5 jobs done" : "Waiting for the next PS5 job";
    const sub = cur && ps5State(cur) === "deleting" ? esc(cur.note || "") : `${doneN} of ${pjobs.length} done${b.sendSpeed && cur ? ", " + size(b.sendSpeed) + "/s" : ""}${b.sendEta && cur ? ", " + dur(b.sendEta) + " left" : ""}${b.sendSlow && cur ? ", " + esc(b.sendSlow) : ""}`;
    lanes.push(`<div class="lane"><div class="what">${what}<small>${sub}</small></div>
      <div class="track send"><i style="width:${pct / 10}%"></i></div>
      ${b.ps5Busy ? `<button class="btn danger sm" data-act="stopps5">Stop</button>` : `<span></span>`}</div>`);
  }
  dock.innerHTML = lanes.join("");
}

// ---------------- settings ----------------
const PS5_QUICK = [["/data/etaHEN/games", "Internal (etaHEN games)"], ["/mnt/usb0", "USB drive 1"], ["/mnt/usb1", "USB drive 2"], ["/mnt/ext0", "Extended storage"]];
let setTab = "ps5";
function showSetTab(t) {
  setTab = t;
  $$("#setnav button").forEach(b => b.dataset.settab === t ? b.setAttribute("aria-current", "true") : b.removeAttribute("aria-current"));
  $$("#view-settings .set").forEach(s => s.hidden = s.dataset.tab !== t);
  const sc = $("#view-settings .scroll"); if (sc) sc.scrollTop = 0;
}
$("#setnav").onclick = e => { const b = e.target.closest("[data-settab]"); if (b) showSetTab(b.dataset.settab); };
showSetTab("ps5");
function renderSettings() {
  for (const [id, v] of [["#ps5ip", S.settings.PS5IP || ""], ["#ps5port", String(S.settings.PS5Port || 2121)], ["#ps5dir", S.settings.PS5Dir || ""]])
    if (document.activeElement !== $(id)) $(id).value = v;
  renderDrives();
  $("#ps5quick").hidden = drives().length > 0;
  $("#ps5quick").innerHTML = PS5_QUICK.map(([p, l]) => `<button data-quick="${esc(p)}" aria-pressed="${S.settings.PS5Dir === p}">${esc(l)}<i>${esc(p)}</i></button>`).join("");
  $("#sendAfter").checked = !!S.settings.SendAfter;
  $("#useP5U").checked = !S.settings.PlainFTP;
  $("#fastSend").checked = !!S.settings.FastSend; $("#fastOpts").hidden = !S.settings.FastSend;
  if (document.activeElement !== $("#sendStreams")) $("#sendStreams").value = String(S.settings.SendStreams || 1);
  if (document.activeElement !== $("#sendAtOnce")) $("#sendAtOnce").value = String(S.settings.SendAtOnce || 2);
  renderPayloads();
  const fw = $("#fw");
  if (document.activeElement !== fw) fw.value = S.settings.ConsoleFW || "";
  $("#showMissing").checked = !!S.settings.ShowMissing;
  $("#storeInfo").checked = S.settings.StoreInfo !== false;
  renderRemote();
  $("#ignored").innerHTML = (S.settings.Ignored || []).map((n, i) => `<span>${esc(n)}<button data-unignore="${i}" aria-label="Remove ${esc(n)}">${ICON.x}</button></span>`).join("") || `<p>Nothing is excluded.</p>`;
  $("#about").innerHTML = `<b>Loadout ${esc(S.version)}</b><br>Settings and the library are saved in your AppData folder.<br>Games are sent with <b>PS5Upload</b> by PhantomPtr (GPL-3.0), included unchanged. Its license and a link to its source are in the Loadout program folder.`;
}

function renderRemote() {
  const r = S.remote || {};
  $("#remoteOn").checked = !!r.on;
  for (const [id, v] of [["#remotePin", S.settings.RemotePIN || ""], ["#remotePort", String(r.port || 8787)], ["#alertNtfy", S.settings.AlertNtfy || ""], ["#alertDiscord", S.settings.AlertDiscord || ""]])
    if (document.activeElement !== $(id)) $(id).value = v;
  $("#closeToTray").checked = !!S.closeToTray; $("#keepAwake").checked = !!S.keepAwake; { const st = S.awakeState === "on" ? "Right now: keeping this PC and its screen awake for the running job." : S.awakeState === "partial" ? "Right now: Windows refused part of the keep-awake request, so it may still sleep. Restarting Loadout usually fixes this." : ""; $("#awakeNotes").innerHTML = (S.keepAwake ? [st, ...(S.awakeNotes || [])] : []).filter(Boolean).map(t => `<p>${esc(t)}</p>`).join(""); }
  $("#trayset").hidden = !S.hasTray;
  let h = "";
  if (r.err) h = `<p class="warnline">${esc(r.err)}</p>`;
  else if (r.running) {
    const others = (r.urls || []).filter(u => u !== r.tailscale);
    h = `<div class="remurls">
      ${r.tailscale ? `<div class="remurl best"><span><b>From anywhere (Tailscale)</b><i>${esc(r.tailscale)}</i></span><button class="btn ghost sm" data-copyurl="${esc(r.tailscale)}">Copy</button></div>`
        : `<p class="hint2 note">To reach it away from home, install the free <b>Tailscale</b> app on this PC and on your phone and sign in to both with the same account. A "from anywhere" address will show up here. Nothing gets opened to the internet.</p>`}
      ${others.map(u => `<div class="remurl"><span><b>On your home Wi-Fi</b><i>${esc(u)}</i></span><button class="btn ghost sm" data-copyurl="${esc(u)}">Copy</button></div>`).join("")}
      </div><p class="hint2">Open the address in your phone's browser and enter the PIN. If Windows asks about the firewall the first time, choose Allow. This PC has to be on with Loadout running (it can sit in the tray).</p>`;
  } else if (r.on) h = `<p class="hint2">Starting the phone page…</p>`;
  $("#remoteInfo").innerHTML = h;
}
let PROF = null, profPick = null; // profPick: { uid, path, preview, mode } while choosing a picture
function profSay(msg, bad) { const st = $("#profStatus"); st.className = "result resultline" + (bad ? " bad" : ""); st.textContent = msg; }
async function profScan(quiet) {
  if (!quiet) profSay("Asking the PS5 for its users. The first time can take half a minute…");
  $("#profScan").disabled = true;
  try { PROF = await api("prof/list", {}); if (!quiet) profSay(PROF.length ? "" : "The PS5 didn't list any users."); }
  catch (e) { profSay(e.message, true); }
  $("#profScan").disabled = false;
  renderProfiles();
}
function renderProfiles() {
  if (!PROF) { $("#profList").innerHTML = ""; return; }
  $("#profList").innerHTML = PROF.map(u => {
    const pk = profPick && profPick.uid === u.uid ? profPick : null, pic = pk ? pk.preview : u.picture;
    return `<div class="set profcard" data-uid="${u.uid}">
      ${pic ? `<img class="profpic" alt="" src="${esc(pic)}">` : `<div class="profpic ph"><svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 20c1.5-4 4.5-6 8-6s6.5 2 8 6"/></svg></div>`}
      <div class="profbody">
        <div class="addrow"><input class="profname" maxlength="16" value="${esc(u.name)}" aria-label="Profile name"><button class="btn ghost" data-prof="rename">Save name</button></div>
        ${pk ? `<div class="addrow"><label class="inline"><input type="radio" name="pm${u.uid}" value="crop" ${pk.mode !== "fit" ? "checked" : ""}> Fill the square</label><label class="inline"><input type="radio" name="pm${u.uid}" value="fit" ${pk.mode === "fit" ? "checked" : ""}> Show the whole picture</label></div>
          <div class="addrow"><button class="btn primary" data-prof="apply">Use this picture</button><button class="btn ghost" data-prof="cancel">Cancel</button></div>`
        : `<div class="addrow"><button class="btn ghost" data-prof="pick">Change picture</button></div>`}
        <small class="mono">User ${esc("0x" + u.uid.toString(16))}</small>
      </div></div>`;
  }).join("");
}
$("#profScan").onclick = () => profScan();
$("#profList").addEventListener("change", async e => {
  if (e.target.type !== "radio" || !profPick) return;
  profPick.mode = e.target.value;
  try { profPick.preview = (await api("prof/preview", { Path: profPick.path, Mode: profPick.mode })).preview; renderProfiles(); } catch (err) { profSay(err.message, true); }
});
$("#profList").addEventListener("click", async e => {
  const b = e.target.closest("[data-prof]"); if (!b) return;
  const card = b.closest(".profcard"), uid = +card.dataset.uid, u = PROF.find(x => x.uid === uid), what = b.dataset.prof;
  try {
    if (what === "rename") {
      const name = card.querySelector(".profname").value.trim();
      b.disabled = true; await api("prof/rename", { UID: uid, Name: name }); u.name = name; profSay(`Renamed to ${name}.`);
    } else if (what === "pick") {
      const r = await api("prof/pick", { Mode: "crop" });
      if (r.path) { profPick = { uid, path: r.path, preview: r.preview, mode: "crop" }; profSay("This is how it will look. Nothing is sent until you choose Use this picture."); }
    } else if (what === "cancel") { profPick = null; profSay(""); }
    else if (what === "apply") {
      b.disabled = true; profSay("Sending the picture to the PS5…");
      await api("prof/picture", { UID: uid, Name: u.name, Path: profPick.path, Mode: profPick.mode });
      u.picture = profPick.preview; profPick = null; profSay(`Picture set for ${u.name}.`);
    }
  } catch (err) { profSay(err.message, true); }
  renderProfiles();
});

let HOME = null;
async function homeScan() {
  const st = $("#homeStatus"); st.className = "result resultline"; st.textContent = "Reading the home screen database from the PS5…";
  $("#homeScan").disabled = true;
  try {
    const r = await api("home/scan", {});
    HOME = r.data || r;
    st.textContent = `Read ${size(HOME.size)} from the PS5. A backup copy is saved on this PC.`;
  } catch (e) { st.className = "result resultline bad"; st.textContent = e.message; }
  $("#homeScan").disabled = false;
  renderHome();
}
function renderHome() {
  $("#homeCopy").hidden = $("#homeOpen").hidden = !HOME;
  $("#homeUndo").hidden = !(HOME && HOME.canUndo);
  if (!HOME) { $("#homeList").innerHTML = ""; return; }
  const icon = HOME.tables.filter(t => t.name.startsWith("tbl_iconinfo_") && t.titles && t.titles.length);
  const tile = (x, act, label) => `<div class="hometile">
      ${x.icon ? `<img alt="" loading="lazy" src="/cover?k=home:${encodeURIComponent(x.id)}" onerror="this.outerHTML='<div class=&quot;homeph&quot;></div>'">` : `<div class="homeph"><span>${esc((x.name || x.id).replace(/[^A-Za-z0-9 ]/g, "").split(/\s+/).filter(Boolean).slice(0, 2).map(w => w[0]).join("").toUpperCase())}</span></div>`}
      <b title="${esc(x.name || x.id)}">${esc(x.name || x.id)}</b><small class="mono">${esc(x.id)}</small>
      ${act ? `<button class="btn ${act === "show" ? "primary" : "ghost"}" data-home="${act}" data-id="${esc(x.id)}">${label}</button>` : ""}</div>`;
  $("#homeList").innerHTML = (icon.length ? "" : `<div class="set"><p>The database was read, but Loadout didn't find the list of tiles in it. Use "Copy details" and send them over so this can be worked out.</p></div>`)
    + icon.map(t => {
      const shown = t.titles.filter(x => x.shown), ours = t.titles.filter(x => !x.shown && x.byUs), sys = t.titles.filter(x => !x.shown && !x.byUs);
      const who = icon.length > 1 ? ` <small>user ${esc(t.name.replace("tbl_iconinfo_", ""))}</small>` : "";
      return `<div class="homesec"><h3>Showing on the home screen <span class="count">${shown.length}</span>${who}</h3>
        <div class="homegrid">${shown.map(x => tile(x, "hide", "Hide")).join("") || "<p>Nothing is showing.</p>"}</div></div>
        <div class="homesec hid"><h3><svg viewBox="0 0 24 24"><path d="M3 3l18 18"/><path d="M10.6 6.2A9.6 9.6 0 0 1 12 6c5 0 8.5 4 9.5 6-.4.8-1.200 2-2.300 3.100M6.300 7.700C4.400 9 3.100 10.800 2.500 12c1 2 4.500 6 9.500 6 1.300 0 2.500-.300 3.600-.700"/></svg>Hidden by Loadout <span class="count">${ours.length}</span>${who}</h3>
        ${ours.length ? `<div class="homegrid">${ours.map(x => tile(x, "show", "Show again")).join("")}</div>` : `<p>Nothing yet. Tiles you hide move down here, and you can bring them back any time.</p>`}</div>
        ${sys.length ? `<details class="homesys"><summary>${sys.length} that the PS5 itself keeps hidden</summary><div class="homegrid small">${sys.map(x => tile(x, "", "")).join("")}</div></details>` : ""}`;
    }).join("");
}
$("#homeList").addEventListener("click", async e => {
  const b = e.target.closest("[data-home]"); if (!b) return;
  const id = b.dataset.id, hide = b.dataset.home === "hide";
  const t = HOME.tables.flatMap(x => x.titles || []).find(x => x.id === id) || { name: id };
  if (hide && !await confirmBox(`Hide ${esc(t.name || id)}?`, `Loadout will switch this tile off in the PS5's home screen list. A backup is saved on this PC first, and you can bring the tile back from this tab.<br><br>Do this while no game is running on the PS5, then restart the PS5.<br><br>If the PS5 ever shows a database error after a change, use "Undo Loadout's last change" here, or rebuild the database from the PS5's safe mode.`, "Hide it")) return;
  const st = $("#homeStatus"); st.className = "result resultline"; st.textContent = hide ? "Hiding…" : "Bringing it back…";
  b.disabled = true;
  try { const r = await api("home/show", { IDs: [id], Shown: !hide }); await homeScan(); st.textContent = r.msg; }
  catch (err) { st.className = "result resultline bad"; st.textContent = err.message; b.disabled = false; }
});
$("#homeUndo").onclick = async () => {
  if (!await confirmBox("Undo Loadout's last change?", "This sends the copy of the home screen list that Loadout saved just before its last change back to the PS5. Anything the PS5 added to its list since then (a newly installed game, say) may need the PS5 to find it again.", "Undo it")) return;
  const st = $("#homeStatus");
  try { const r = await api("home/undo", {}); await homeScan(); st.className = "result resultline"; st.textContent = r.msg; }
  catch (err) { st.className = "result resultline bad"; st.textContent = err.message; }
};
$("#homeScan").onclick = homeScan;
$("#homeCopy").onclick = () => HOME && act("home/copy", { Text: HOME.report }, "Details copied. Paste them into the chat.");
$("#homeOpen").onclick = () => HOME && act("home/open", { Dir: HOME.backup });

function renderPayloads() {
  const ps = S.payloads || [];
  const pp = $("#payport");
  if (document.activeElement !== pp) pp.value = S.loaderPort || 9021;
  $("#autoShell").checked = !!S.settings.AutoShell;
  $("#paylist").innerHTML = ps.length ? ps.map(p => `<div class="payrow${p.missing ? " gone" : ""}">
      <span class="pname">${esc(p.name)}${p.known ? ` <em>${{ shsrv: "free-space shell", ftpsrv: "FTP server" }[p.known] || ""}</em>` : ""}<i>${esc(p.missing ? "File not found: " + p.path : p.path)}</i></span>
      <span class="psize">${p.size > 0 ? size(p.size) : ""}</span>
      <button class="btn sm primary" data-paysend="${esc(p.path)}" ${p.missing || !S.settings.PS5IP ? "disabled" : ""} title="${S.settings.PS5IP ? "Send to " + esc(S.settings.PS5IP) + ":" + (p.port || S.loaderPort) : "Set your PS5's IP address first"}">Send</button>
      <button class="btn icon sm" data-payrm="${esc(p.path)}" aria-label="Remove ${esc(p.name)} from the list" title="Remove from the list (the file stays on your PC)">${ICON.x}</button></div>`).join("")
    : `<p class="hint2">No payloads yet. Add your own .elf files, or click Get shsrv to download the one that shows free space.</p>`;
}

function renderDrives() {
  const el = $("#ps5drives"), p = S.ps5 || {};
  if (!S.settings.PS5IP) { el.innerHTML = ""; return; }
  const ds = drives(), def = defDrive();
  let h = `<div class="drhead"><b>Drives on your PS5</b><button class="btn ghost sm" id="ps5refresh" ${p.busy ? "disabled" : ""}>${p.busy ? "Checking…" : "Refresh"}</button></div>`;
  if (!ds.length) h += `<p class="hint2">${p.err ? "Couldn't reach the PS5: " + esc(p.err) : "Looking for drives…"}</p>`;
  h += ds.map(d => {
    const used = d.total > 0 ? Math.round((d.total - d.free) * 100 / d.total) : 0;
    return `<label class="drive${d.root === def ? " on" : ""}"><input type="radio" name="ps5drive" data-drive="${esc(d.root)}" ${d.root === def ? "checked" : ""}><span class="dot"></span>
      <span class="dname">${esc(d.label)}${d.empty ? ` <small>empty, may not be plugged in</small>` : ""}<i>${esc(d.dir)}</i></span>
      ${d.free >= 0 ? `<span class="dbar"><i class="${d.free < 60e9 ? "low" : ""}" style="width:${used}%"></i></span><span class="dfree"><b>${size(d.free)}</b> free of ${size(d.total)}</span>` : `<span class="dfree dim">free space unknown</span>`}</label>`;
  }).join("");
  if (ds.length) h += `<p class="hint2">The checked drive is where games go by default. You can pick a different drive per game on the Build & send screen.</p>`;
  if (ds.length && !p.shell) h += `<p class="hint2 note">To see free space, load the <b>shsrv</b> payload on the PS5. It runs a small shell on port 2323 that Loadout asks for disk usage. Drives are found without it, but free space and the "not enough room" check need it. If shsrv isn't answering, Loadout also tries the PS5Upload helper.${p.spaceErr ? `<br><small>Last try: ${esc(p.spaceErr)}</small>` : ""}
    <button class="btn sm primary" data-shsrvnow>${(S.payloads || []).some(x => x.known === "shsrv" && !x.missing) ? "Send shsrv now" : "Download and send shsrv"}</button></p>`;
  if (ds.length && p.online === false) h += `<p class="warnline">The PS5 isn't answering right now, so this is the list from last time.</p>`;
  el.innerHTML = h;
}

// ---------------- modal + toast ----------------
function modal(html, buttons) {
  return new Promise(res => {
    $("#modalBody").innerHTML = html;
    $("#modalBtns").innerHTML = buttons.map((b, i) => `<button class="btn ${b.cls || "ghost"}" data-i="${i}">${esc(b.label)}</button>`).join("");
    $("#modal").hidden = false;
    const inp = $("#modalBody input");
    (inp || $("#modalBtns .btn:last-child")).focus();
    if (inp) inp.select();
    const done = v => { $("#modal").hidden = true; document.removeEventListener("keydown", key, true); res(v); };
    const key = e => {
      if (e.key === "Escape") { e.preventDefault(); done(null); }
      if (e.key === "Enter" && inp) { e.preventDefault(); done({ i: buttons.length - 1, value: inp.value }); }
    };
    document.addEventListener("keydown", key, true);
    $("#modalBtns").onclick = e => { const b = e.target.closest("button"); if (b) done({ i: +b.dataset.i, value: inp ? inp.value : null }); };
    $("#modalBody").onclick = e => { const d = e.target.closest("[data-dir]"); if (d) done({ dir: d.dataset.dir }); const p = e.target.closest("[data-pick]"); if (p) done({ pick: p.dataset.pick }); };
  });
}
async function confirmBox(title, text, okLabel, cls) {
  const r = await modal(`<h3>${esc(title)}</h3><p>${text}</p>`, [{ label: "Cancel" }, { label: okLabel, cls: cls || "primary" }]);
  return r && r.i === 1;
}
function toast(msg, bad) {
  const t = document.createElement("div");
  t.className = "toast" + (bad ? " bad" : ""); t.textContent = msg;
  $("#toasts").append(t);
  setTimeout(() => t.remove(), bad ? 6000 : 3800);
}

// ---------------- actions ----------------
async function pickGames() {
  const p = await api("pick", { title: "Pick the folder that holds your game folders", start: S.settings.Folder }).catch(e => toast(e.message, true));
  if (p) act("scan", { folder: p });
}
async function buildKeys(keys) {
  if (S.build.building) { toast("A build is already running. Wait for it to finish.", true); return; }
  if (!S.outDir) { toast("Choose where to save images on the Build & send tab first.", true); return; }
  const send = keys.filter(k => bsel.has(k) ? bsend.has(k) : defaultSend());
  const dests = {};
  send.forEach(k => dests[k] = bsel.has(k) ? sendDrive(k) : defDrive());
  await act("build", { keys, send, dests, overwrite: $("#overwrite").checked });
}
async function sendItems(list, ask) {
  if (!S.settings.PS5IP) { toast("Set your PS5's IP address first.", true); showView("settings"); showSetTab("ps5"); $("#ps5ip").focus(); return; }
  let pick = null;
  if (ask && drives().length > 1) {
    const it = list[0], sz = it.imageSize || it.size || 0;
    const r = await modal(`<h3>Send to which drive?</h3><p>${esc(it.name || it.current)}${sz ? ", " + size(sz) : ""}</p><ul class="ftplist drivepick">${drives().map(d =>
      `<li><button data-pick="${esc(d.root)}" ${d.free >= 0 && sz > d.free ? "disabled" : ""}>${ICON.folder}<span>${esc(d.label)}<i>${esc(d.dir)}</i></span><em>${d.free >= 0 ? size(d.free) + " free" : ""}</em></button></li>`).join("")}</ul>`, [{ label: "Cancel" }]);
    if (!r || !r.pick) return;
    pick = r.pick;
  }
  const items = list.filter(i => i.image).map(i => ({ key: i.key, name: i.name || i.current, image: i.image, drive: pick || (bsel.has(i.key) ? sendDrive(i.key) : defDrive()) }));
  if (!items.length) { toast("Build the image first, then send it.", true); return; }
  act("send", { items });
}
async function browsePS5(start) {
  let dir = start || S.settings.PS5Dir || "/";
  const ip = $("#ps5ip").value.trim(), port = +$("#ps5port").value || 2121;
  for (;;) {
    let res;
    try { res = await api("ps5/list", { ip, port, dir }); } catch (e) { toast(e.message, true); return; }
    const up = dir === "/" ? "" : dir.replace(/\/[^/]+\/?$/, "") || "/";
    const rows = res.entries.filter(e => e.isDir).map(e => `<li><button data-dir="${esc((dir === "/" ? "" : dir) + "/" + e.name)}">${ICON.folder}${esc(e.name)}</button></li>`).join("");
    const files = res.entries.filter(e => !e.isDir && /\.exfat$/i.test(e.name)).length;
    const r = await modal(`<h3>Pick a folder on the PS5</h3><p class="mono">${esc(dir)}</p>
      ${res.missing ? `<p class="warnline">This folder doesn't exist yet. Loadout creates it when it sends the first image.</p>` : ""}
      <ul class="ftplist">${up ? `<li><button data-dir="${esc(up)}">${ICON.arrow}<span>Up one folder</span></button></li>` : ""}${rows || (res.missing ? "" : `<li class="none">No folders in here</li>`)}</ul>
      ${files ? `<p class="hint2">${files} exFAT image${files === 1 ? "" : "s"} already in this folder</p>` : ""}`,
      [{ label: "Cancel" }, { label: "Use this folder", cls: "primary" }]);
    if (!r) return;
    if (r.dir) { dir = r.dir; continue; }
    if (r.i === 1) { $("#ps5dir").value = dir; act("settings", { ps5Dir: dir }); }
    return;
  }
}
async function deleteFromPS5(it, paths) {
  const copies = (it.onPs5List || []).filter(c => paths.includes(c.path));
  if (!copies.length) return;
  const left = (it.onPs5List || []).length - copies.length;
  const onlyCopy = !it.image && !it.present;
  const ok = await confirmBox(`Delete ${it.name || it.current} from the PS5?`,
    `${copies.map(c => `<b>${esc(c.drive)}</b> <span class="mono">${esc(c.path)}</span>${c.size ? ` (${esc(size(c.size))})` : ""}`).join("<br>")}
    <br><br>This permanently deletes it from the PS5. It doesn't go to a Recycle Bin.
    ${onlyCopy ? `<br><b style="color:var(--coral)">You don't have an image or folder of this game on your PC, so this would be your only copy.</b>` : it.image ? " Your image on the PC stays, so you can send it again later." : ""}
    ${left ? `<br>${left} other cop${left === 1 ? "y stays" : "ies stay"} on the PS5.` : ""}
    <br>Close the game on the PS5 first if it's running.`, "Delete from PS5", "danger solid");
  if (ok) act("ps5/delete", { paths: copies.map(c => c.path) });
}
async function removeFolder(it) {
  if (S.build.building) { toast("Wait for the current build to finish first.", true); return; }
  const ok = await confirmBox("Remove the game folder?",
    `Loadout first checks every file in <b>${esc(it.image.split(/[\\/]/).pop())}</b> against the folder. Only if everything matches does the folder go to the Recycle Bin. The game stays in your library as exFAT only.`,
    "Check and remove");
  if (ok) act("remove", { keys: [it.key] });
}
async function fixName(k) {
  const it = S.items.find(i => i.key === k); if (!it) return;
  const r = await modal(`<h3>Game name</h3><p>${esc(it.current)}${it.titleId ? ` (${esc(it.titleId)})` : ""}</p><input value="${esc(it.name)}" spellcheck="false">`,
    [{ label: "Cancel" }, { label: "Save name", cls: "primary" }]);
  if (r && r.i === 1 && r.value.trim()) act("setname", { key: k, name: r.value.trim() }, "Name saved");
}

document.addEventListener("click", async e => {
  const nav = e.target.closest(".navbtn");
  if (nav) { svGame = ""; showView(nav.dataset.view); if (nav.dataset.view === "saves") renderSaves(); return; }
  const f = e.target.closest("[data-f]");
  if (f) { filter = f.dataset.f; renderLibrary(); return; }
  const lay = e.target.closest("[data-layout]");
  if (lay) { S.settings.View = lay.dataset.layout; renderLibrary(); act("settings", { view: lay.dataset.layout }); return; }
  const tok = e.target.closest("[data-token]");
  if (tok) {
    const p = $("#" + (tok.dataset.target || "pattern")), s = p.selectionStart ?? p.value.length;
    p.value = p.value.slice(0, s) + tok.dataset.token + p.value.slice(p.selectionEnd ?? s);
    p.focus(); p.id === "dlcPattern" ? saveDLCPattern() : savePattern(); return;
  }
  const un = e.target.closest("[data-unignore]");
  if (un) { const l = [...S.settings.Ignored]; l.splice(+un.dataset.unignore, 1); act("settings", { ignored: l }); return; }
  const b = e.target.closest("[data-act]");
  if (!b) return;
  const k = b.dataset.k, it = k && S.items.find(i => i.key === k);
  switch (b.dataset.act) {
    case "pick": return pickGames();
    case "open": return openDrawer(k);
    case "folder": return act("open", { path: it.path });
    case "showimg": return act("open", { path: it.image, select: true });
    case "showpkg": return act("open", { path: it.path, select: true });
    case "pkginstall": return act("pkg/install", { Key: k });
    case "build1": return buildKeys([k]);
    case "remove1": return removeFolder(it);
    case "send1":
      if (it.onPs5 && !(await confirmBox("Send it again?", `${esc(it.name)} is already in ${esc(it.onPs5)} on the PS5. Sending again replaces it if it's different, or skips it if it's the same.`, "Send again"))) return;
      return sendItems([it], true);
    case "dlc": return showDLC(it);
    case "gosettings": showView("settings"); showSetTab("ps5"); return;
    case "saves": svGame = b.dataset.id || ""; showView("saves"); renderSaves(); return;
    case "moredesc": { const d = $("#gdesc"); d.classList.toggle("clamp"); b.textContent = d.classList.contains("clamp") ? "Show more" : "Show less"; return; }
    case "storepage": return act("open", { path: b.dataset.u });
    case "refreshinfo": return act("storeinfo/refresh", { key: b.dataset.k });
    case "shot": {
      const owner = S.items.find(i => i.key === b.dataset.k); let n = +b.dataset.n;
      for (;;) {
        const r = await modal(`<img class="bigshot" alt="" src="${artURL(owner, "shot" + n)}">`, [{ label: "Previous" }, { label: "Next" }, { label: "Close", cls: "primary" }]);
        if (!r || r.i === 2) return;
        n = (n + (r.i === 1 ? 1 : owner.shots - 1)) % owner.shots;
      }
    }
    case "ps5del": return deleteFromPS5(it, [b.dataset.p]);
    case "ps5move": { const r = ps5Rows().find(x => x.path === b.dataset.p); return r && movePS5Rows([r]); }
    case "ps5help": showView("settings"); showSetTab("send"); return;
    case "rename1": return fixName(k);
    case "copyid": return act("copy", { text: b.dataset.id }, "Copied " + b.dataset.id);
    case "cancel": return stopLane("");
    case "stopbuild": return stopLane("build");
    case "stopps5": return stopLane("ps5");
  }
});

document.addEventListener("change", e => {
  const t = e.target;
  if (t.dataset.ren) { act("check", { keys: [t.dataset.ren], checked: t.checked }); return; }
  if (t.dataset.bld) {
    if (t.checked) { bsel.add(t.dataset.bld); if (S.settings.SendAfter && S.settings.PS5IP) bsend.add(t.dataset.bld); }
    else { bsel.delete(t.dataset.bld); bsend.delete(t.dataset.bld); }
    renderBuild(); return;
  }
  if (t.dataset.bsend) { t.checked ? bsend.add(t.dataset.bsend) : bsend.delete(t.dataset.bsend); renderBuild(); return; }
});

let lastPS5Peek = 0;
function showView(v) {
  view = v;
  // opening On PS5 rescans, so games deleted on the console drop off right away
  if (v === "ps5" && S && S.settings.PS5IP && Date.now() - lastPS5Peek > 10000) {
    lastPS5Peek = Date.now();
    api("ps5/drives", {}).then(() => poll(true)).catch(() => poll(true));
  }
  if (v === "payloads" && S && S.settings.PS5IP && !AUTO) autoScan();
  if (v === "profiles") renderProfiles();
  if (v === "emu" && S && S.settings.PS5IP) emuLoad(); else if (v === "emu") renderEmu();
  if (v === "files" || v === "homebrew") { FX.mode = v === "homebrew" ? "homebrew" : "games"; FX.entries = []; FX.roots = []; if (S && S.settings.PS5IP) fxLoad(FX.dirs[FX.mode] || ""); else renderFiles(); }
  if (v === "saves" && S && S.settings.PS5IP && Date.now() - lastSavePeek > 15000) { lastSavePeek = Date.now(); api("saves/scan", {}).then(() => poll(true)).catch(() => {}); }
  $$(".navbtn").forEach(b => b.dataset.view === (v === "homebrew" ? "emu" : v) ? b.setAttribute("aria-current", "page") : b.removeAttribute("aria-current"));
  document.body.dataset.side = ["library", "rename", "build", "settings"].includes(v) ? "pc" : "ps5";
  $$(".view").forEach(s => s.hidden = s.id !== "view-" + (v === "homebrew" ? "files" : v));
  closeDrawer();
}

let patTimer, dlcTimer;
function saveDLCPattern() { clearTimeout(dlcTimer); dlcTimer = setTimeout(() => act("settings", { dlcPattern: $("#dlcPattern").value }), 250); }
function savePattern() { clearTimeout(patTimer); patTimer = setTimeout(() => act("settings", { pattern: $("#pattern").value }), 250); }

$("#pickFolder").onclick = pickGames;
$("#rescan").onclick = () => act("scan", {});
$("#q").oninput = () => renderLibrary();
$("#pattern").oninput = savePattern;
$("#nameEditBtn").onclick = e => {
  const ed = $("#nameEdit"); ed.hidden = !ed.hidden;
  e.target.textContent = ed.hidden ? "Change" : "Done";
  e.target.setAttribute("aria-expanded", String(!ed.hidden));
  renderRename();
};
$("#nameEdit").addEventListener("change", e => {
  const t = e.target;
  if (t.name === "gp") { gameOwn = !t.value; if (t.value) act("settings", { pattern: t.value }); renderRename(); if (gameOwn) $("#pattern").focus(); }
  if (t.name === "dp") { dlcOwn = !t.value; if (t.value) act("settings", { dlcPattern: t.value }); renderRename(); if (dlcOwn) $("#dlcPattern").focus(); }
});
$("#dlcPattern").oninput = saveDLCPattern;
$("#safe").onchange = e => act("settings", { safe: e.target.checked });
$("#wrap").onchange = e => act("settings", { wrapLoose: e.target.checked });
$("#movedlc").onchange = e => act("settings", { moveDlc: e.target.checked });
$("#renAll").onchange = e => act("check", { keys: S.items.filter(i => i.present && (i.canRename || i.checked)).map(i => i.key), checked: e.target.checked });
$("#doRename").onclick = async () => {
  const sel = S.items.filter(i => i.checked);
  const loose = sel.filter(i => i.wrap).length;
  const dlcN = sel.filter(i => i.isDlc).length;
  const names = sel.filter(i => !i.isDlc && i.newName !== i.current).length;
  let txt = `${names} item${names === 1 ? "" : "s"} will be renamed.`;
  if (dlcN) txt += ` ${dlcN} DLC will be moved into ${dlcN === 1 ? "its game's folder" : "their games' folders"}.`;
  if (loose) txt += ` ${loose} game folder${loose === 1 ? " has" : "s have"} loose files that will be moved into a PPSA#####-app folder.`;
  if (await confirmBox("Rename now?", esc(txt) + " You can undo this afterwards.", "Rename")) act("rename");
};
$("#undo").onclick = async () => {
  if (await confirmBox("Undo the last rename?", `This puts ${S.canUndo} change${S.canUndo === 1 ? "" : "s"} back the way they were.`, "Undo")) act("undo");
};
const defaultSend = () => !!(S.settings.SendAfter && S.settings.PS5IP);
$("#bldAll").onchange = e => {
  games().filter(i => (i.present || i.exfatOnly) && !i.isPkg && pickable(i)).forEach(i => {
    if (e.target.checked) { if (!bsel.has(i.key) && defaultSend()) bsend.add(i.key); bsel.add(i.key); }
    else { bsel.delete(i.key); bsend.delete(i.key); }
  });
  renderBuild();
};
const sendShsrv = () => {
  const have = (S.payloads || []).find(x => x.known === "shsrv" && !x.missing);
  return have ? act("payload/send", { path: have.path }) : act("payload/get", { name: "shsrv", send: true });
};
$("#ps5drives").addEventListener("click", e => { if (e.target.closest("[data-shsrvnow]")) sendShsrv(); });
$("#payset").addEventListener("click", async e => {
  const t = e.target.closest("button");
  if (!t) return;
  if (t.id === "payAdd") return act("payload/add", {});
  if (t.dataset.payget) { t.disabled = true; t.textContent = "Downloading…"; await act("payload/get", { name: t.dataset.payget }); t.disabled = false; t.textContent = "Get " + t.dataset.payget; return; }
  if (t.dataset.paysend) { t.disabled = true; await act("payload/send", { path: t.dataset.paysend }); return; }
  if (t.dataset.payrm) return act("payload/remove", { path: t.dataset.payrm });
});
$("#payport").onchange = e => act("settings", { payloadPort: +e.target.value || 9021 });
$("#autoShell").onchange = e => {
  if (e.target.checked && !(S.payloads || []).some(x => x.known === "shsrv" && !x.missing)) { act("payload/get", { name: "shsrv" }); }
  act("settings", { autoShell: e.target.checked });
};
$("#pcDrives").onclick = async e => {
  const s = e.target.closest("[data-pcsetup]"), u = e.target.closest("[data-pcuse]"), o = e.target.closest("[data-pcopen]");
  if (o) return act("open", { path: o.dataset.pcopen });
  if (s || u) {
    const root = (s || u).dataset.pcsetup || (s || u).dataset.pcuse;
    if (u) return act("pcdrive/setup", { root, use: true });
    const r = await modal(`<h3>Set up this drive for the PS5?</h3><p>Loadout creates an <b>etaHEN\\games</b> folder on it, which is where ShadowMount+ looks for games on a USB drive. Nothing already on the drive is changed or deleted.</p><p>You can also have new images built straight onto it.</p>`,
      [{ label: "Cancel" }, { label: "Just create the folder" }, { label: "Create it and save images there", cls: "primary" }]);
    if (!r || r.i === 0) return;
    return act("pcdrive/setup", { root, use: r.i === 2 });
  }
};
$("#spot").addEventListener("click", e => {
  const d = e.target.closest("[data-spot]");
  if (d) { spotIdx = +d.dataset.spot; $("#spot").dataset.sig = ""; renderLibrary(); }
});
$("#storeInfo").onchange = e => act("settings", { storeInfo: e.target.checked });
$("#remoteOn").onchange = e => {
  const pin = $("#remotePin").value.trim();
  if (e.target.checked && pin.length < 4) { toast("Type a PIN of at least 4 digits first.", true); e.target.checked = false; $("#remotePin").focus(); return; }
  act("settings", { remoteOn: e.target.checked, remotePin: pin, remotePort: +$("#remotePort").value || 8787 });
};
let remTimer;
const saveRemote = () => { clearTimeout(remTimer); remTimer = setTimeout(() => act("settings", { remotePin: $("#remotePin").value.trim(), remotePort: +$("#remotePort").value || 8787 }), 600); };
$("#remotePin").oninput = saveRemote; $("#remotePort").oninput = saveRemote;
$("#remoteInfo").addEventListener("click", e => { const b = e.target.closest("[data-copyurl]"); if (b) act("copy", { text: b.dataset.copyurl }, "Copied. Paste it into your phone's browser."); });
let alertTimer;
const saveAlerts = () => { clearTimeout(alertTimer); alertTimer = setTimeout(() => act("settings", { alertNtfy: $("#alertNtfy").value.trim(), alertDiscord: $("#alertDiscord").value.trim() }), 500); };
$("#alertNtfy").oninput = saveAlerts; $("#alertDiscord").oninput = saveAlerts;
$("#alertTest").onclick = async () => {
  const out = $("#alertResult"); out.className = "result"; out.textContent = "Sending…";
  clearTimeout(alertTimer);
  try {
    await api("settings", { alertNtfy: $("#alertNtfy").value.trim(), alertDiscord: $("#alertDiscord").value.trim() });
    const r = await api("alert/test", {}); out.className = "result ok"; out.textContent = r;
  } catch (e) { out.className = "result bad"; out.textContent = e.message; }
};
$("#closeToTray").onchange = e => act("settings", { closeToTray: e.target.checked });
$("#keepAwake").onchange = e => act("settings", { keepAwake: e.target.checked });
$("#view-ps5").addEventListener("click", e => {
  const f = e.target.closest("[data-psdrive]");
  if (f) { psDrive = f.dataset.psdrive; renderPS5(); return; }
  const d = e.target.closest("[data-psdel]");
  if (d) { const r = ps5Rows().find(x => x.path === d.dataset.psdel); if (r) deletePS5Rows([r]); return; }
  const m = e.target.closest("[data-psmove]");
  if (m) { const r = ps5Rows().find(x => x.path === m.dataset.psmove); if (r) movePS5Rows([r]); return; }
});
$("#ps5List").addEventListener("change", e => {
  const t = e.target;
  if (t.dataset && t.dataset.psel) { t.checked ? psel.add(t.dataset.psel) : psel.delete(t.dataset.psel); renderPS5(); }
});
$("#ps5All").onchange = e => {
  ps5Rows().filter(r => r.kind !== "installed" && (!psDrive || r.drive === psDrive)).forEach(r => e.target.checked ? psel.add(r.path) : psel.delete(r.path));
  renderPS5();
};
$("#ps5DelSel").onclick = () => $("#ps5DelSel").dataset.mode === "stop" ? stopLane("ps5") : deletePS5Rows(ps5Rows().filter(r => psel.has(r.path)));
$("#ps5MoveSel").onclick = () => movePS5Rows(ps5Rows().filter(r => psel.has(r.path)));
$("#ps5Rescan").onclick = async () => {
  $("#ps5Rescan").disabled = true;
  await act("ps5/drives", {}, d => d.online ? "PS5 rescanned." : "Couldn't reach the PS5.");
};
$("#bldDest").onchange = e => { batchDrive = e.target.value; bdest.clear(); renderBuild(); };
$("#bldList").addEventListener("change", e => { const t = e.target; if (t.dataset && t.dataset.bdest) { bdest.set(t.dataset.bdest, t.value); t.blur(); renderBuild(); } });
$("#ps5drives").addEventListener("change", e => { const t = e.target; if (t.dataset && t.dataset.drive) { batchDrive = null; bdest.clear(); act("settings", { ps5Drive: t.dataset.drive }); } });
$("#ps5drives").addEventListener("click", async e => {
  if (!e.target.closest("#ps5refresh")) return;
  e.target.closest("#ps5refresh").disabled = true;
  await act("ps5/drives", {}, d => d.online ? `Found ${d.drives.length} drive${d.drives.length === 1 ? "" : "s"} on the PS5.` : "Couldn't reach the PS5.");
});
$("#bldSendAll").onchange = e => { bsel.forEach(k => e.target.checked ? bsend.add(k) : bsend.delete(k)); renderBuild(); };
$("#bldMissing").onclick = () => {
  bsel = new Set(games().filter(i => i.present && i.canBuild && !i.image).map(i => i.key));
  bsend = new Set(defaultSend() ? bsel : []);
  renderBuild();
};
$("#doBuild").onclick = () => $("#doBuild").dataset.mode === "stop" ? stopLane("build") : buildKeys([...bsel].filter(k => (games().find(i => i.key === k) || {}).canBuild));
$("#nukeBig").onchange = async e => {
  if (e.target.checked && !(await confirmBox("Delete game folders for good?", "With this on, each game folder is deleted for good once its image passes the check. It does not go to the Recycle Bin, so you won't be able to get the folder back; the image is your only copy.", "Turn it on", "danger solid"))) { e.target.checked = false; return; }
  act("settings", { nukeBig: e.target.checked });
};
$("#removeAfter").onchange = e => act("settings", { removeAfter: e.target.checked });
$("#pickOut").onclick = async () => {
  const p = await api("pick", { title: "Where should the .exfat images go?", start: S.outDir }).catch(e => toast(e.message, true));
  if (p) act("settings", { imageOut: p });
};
$("#openOut").onclick = () => S.outDir && act("open", { path: S.outDir, create: true });
let fwTimer;
$("#fw").oninput = e => { clearTimeout(fwTimer); fwTimer = setTimeout(() => act("settings", { consoleFw: e.target.value.trim() }), 300); };
let ps5Timer;
const savePS5 = () => { clearTimeout(ps5Timer); ps5Timer = setTimeout(() => act("settings", { ps5Ip: $("#ps5ip").value.trim(), ps5Port: +$("#ps5port").value || 2121, ps5Dir: $("#ps5dir").value.trim() }), 400); };
["#ps5ip", "#ps5port", "#ps5dir"].forEach(id => $(id).oninput = savePS5);
$("#ps5browse").onclick = () => browsePS5($("#ps5dir").value.trim());
$("#ps5quick").onclick = e => { const b = e.target.closest("[data-quick]"); if (b) { $("#ps5dir").value = b.dataset.quick; act("settings", { ps5Dir: b.dataset.quick }); } };
$("#fastSend").onchange = e => act("settings", { fastSend: e.target.checked });
$("#useP5U").onchange = e => act("settings", { plainFTP: !e.target.checked });
$("#sendStreams").onchange = e => act("settings", { sendStreams: +e.target.value });
$("#sendAtOnce").onchange = e => act("settings", { sendAtOnce: +e.target.value });
$("#sendAfter").onchange = e => {
  if (e.target.checked && !S.settings.PS5IP) { toast("Set your PS5's IP address first.", true); e.target.checked = false; return; }
  act("settings", { sendAfter: e.target.checked });
};
$("#recvtest").onclick = async () => {
  const out = $("#sendResult"); out.className = "result"; out.textContent = "Checking the fast receiver on the PS5, about 25 seconds…";
  $("#recvtest").disabled = true;
  try {
    const r = await api("ps5/recvtest", {});
    out.className = "result " + (r.ok ? "ok" : "warnres"); out.textContent = r.msg;
  } catch (e) { out.className = "result bad"; out.textContent = e.message; }
  $("#recvtest").disabled = false;
};
$("#ps5speed").onclick = async () => {
  const out = $("#ps5result"); out.className = "result"; out.textContent = "Sending a test file to the PS5 for a few seconds…";
  $("#ps5speed").disabled = true;
  try {
    const r = await api("ps5/speed", {});
    out.className = "result " + (r.mbs >= 40 ? "ok" : "warnres");
    out.innerHTML = `<b>${r.mbs.toFixed(1)} MB/s</b>. ${esc(r.verdict)}`;
  } catch (e) { out.className = "result bad"; out.textContent = e.message; }
  $("#ps5speed").disabled = false;
};
$("#ps5clean").onclick = async () => {
  const out = $("#sendResult"); out.className = "result"; out.textContent = "Checking the PS5…";
  try {
    const gone = await api("ps5/cleanup", {});
    out.className = "result ok";
    out.textContent = gone.length ? `Removed ${gone.length} unfinished upload${gone.length === 1 ? "" : "s"}: ${gone.join(", ")}` : "No unfinished uploads on the PS5.";
  } catch (e) { out.className = "result bad"; out.textContent = e.message; }
};
$("#ps5test").onclick = async () => {
  const out = $("#ps5result"); out.className = "result"; out.textContent = "Connecting…";
  try {
    const r = await api("ps5/list", { ip: $("#ps5ip").value.trim(), port: +$("#ps5port").value || 2121, dir: $("#ps5dir").value.trim() || "/" });
    out.className = "result ok";
    out.textContent = r.missing ? `Connected. ${r.dir} doesn't exist yet, it'll be created on the first send.` : `Connected. ${r.dir} has ${r.entries.length} item${r.entries.length === 1 ? "" : "s"}.`;
  } catch (e) { out.className = "result bad"; out.textContent = e.message; }
};
$("#doSend").onclick = () => $("#doSend").dataset.mode === "stop" ? stopLane("ps5") : sendItems(games().filter(i => bsel.has(i.key)));
$("#showMissing").onchange = e => act("settings", { showMissing: e.target.checked });
$("#addIgnore").onsubmit = e => {
  e.preventDefault();
  const n = $("#ignoreName").value.trim(); if (!n) return;
  $("#ignoreName").value = "";
  act("settings", { ignored: [...S.settings.Ignored, n] });
};
$("#resetIgnore").onclick = () => act("settings", { ignored: S.defaultIgnored });
$("#export").onclick = () => act("export", {}, p => p ? "Saved " + p : "");
$("#clearMissing").onclick = async () => {
  if (await confirmBox("Forget missing games?", "Games that are no longer in the folder are removed from the library. Their cover art is kept.", "Forget them")) act("clear-missing", {}, n => `Forgot ${n} game(s)`);
};
$("#drawerClose").onclick = closeDrawer;
$("#scrim").onclick = closeDrawer;
document.addEventListener("keydown", e => { if (e.key === "Escape" && drawerKey && $("#modal").hidden) closeDrawer(); });

poll(true);
setInterval(() => { if (view === "ps5" && S) renderPS5(); }, 10000);

$("#bldShowDone").onclick = () => { showDone = !showDone; renderBuild(); };

$("#ps5List").addEventListener("click", e => { if (e.target.closest("[data-copyrep]")) { navigator.clipboard.writeText(((S.ps5 || {}).report || []).join("\n")).then(() => toast("Copied.")); } });

// ---------------- autoloaded payloads ----------------
function renderAuto() {
  const el = $("#autoList"); if (!AUTO) { el.innerHTML = ""; return; }
  if (!AUTO.dir) {
    el.innerHTML = `<p class="hint2">Loadout couldn't find an autoloader folder on the PS5. It checked the usual <span class="mono">ps5_autoloader</span> folders and looked for anything named like one on each drive.</p>
      <div class="addrow"><input id="autoDir" class="grow" placeholder="/path/to/your/autoloader/folder" spellcheck="false" value="${esc(S.settings.AutoDir || "")}"><button class="btn ghost" id="autoDirSave">Use this folder</button></div>
      <p class="hint2">If you know where your jailbreak keeps its payloads on the PS5, type the folder here. If not, send me the details so I can add it:</p>
      <details class="scanrep" style="margin:0;max-width:none"><summary style="text-align:left">What Loadout saw on the PS5</summary><pre>${esc((AUTO.seen || []).join("\n") || "Nothing could be listed.")}</pre><button class="btn ghost sm" id="autoCopy">Copy this</button></details>`;
    return;
  }
  const mg = AUTO.manager;
  const rows = AUTO.items.map(it => {
    const ver = it.missing ? "" : it.version ? esc(it.version) : `<span class="dim">doesn't say</span>`;
    const nv = s => (s || "").toLowerCase().replace(/^v/, ""), cur = it.same || (it.version && it.latest && nv(it.version) === nv(it.latest));
    const st = it.missing ? `<span class="autobad">Missing</span>` : cur ? `<span class="autook">Up to date</span>` : it.latest ? `<span class="autonew">Newest is ${esc(it.latest)}</span>` : "";
    return `<div class="autorow"><span class="autoname"><b>${esc(it.name || it.file)}${it.listed ? ` <span class="autotag">Autoloads</span>` : ""}</b><small class="mono">${esc(it.file)}${it.size ? " · " + size(it.size) : ""}${it.source ? " · " + esc(it.source) : ""}${!mg && !it.listed ? " · not in autoload.txt" : ""}</small>${it.desc ? `<small>${esc(it.desc)}</small>` : ""}${it.note ? `<small>${esc(it.note)}</small>` : ""}</span>
      <span class="autover">${ver}</span><span class="autost">${st}</span>
      <span>${it.asset && !cur && !it.missing ? `<button class="btn ghost sm" data-autoupd="${esc(it.file)}" data-path="${esc(it.path)}">Update</button>` : mg && it.latest && !cur && !it.missing ? `<button class="btn ghost sm" data-automgr="1" title="Loadout has no download for this one, so update it in Payload Manager">Open Manager</button>` : ""}</span></div>`;
  }).join("");
  const order = AUTO.order.filter(l => !l.startsWith("!"));
  el.innerHTML = (mg
    ? `<p class="hint2"><b>PS5 Payload Manager</b> found. Folder: <span class="mono">${esc(AUTO.dir)}</span>${order.length ? ` · autoloads in this order: ${order.map(esc).join(", ")}` : " · nothing set to autoload"} <button class="linkbtn" data-automgr="1">Open Payload Manager</button></p>`
    : `<p class="hint2">Folder: <span class="mono">${esc(AUTO.dir)}</span> <button class="linkbtn" id="autoChange">Change</button>${AUTO.order.length ? ` · loads in this order: ${AUTO.order.map(esc).join(", ")}` : " · no autoload.txt found"}</p>`)
    + (() => { const nvv = x => (x || "").toLowerCase().replace(/^v/, ""); const n = AUTO.items.filter(it => it.asset && !it.missing && !(it.same || (it.version && it.latest && nvv(it.version) === nvv(it.latest)))).length; return n > 1 ? `<div class="addrow"><button class="btn primary" id="autoAll">Update all ${n}</button></div>` : ""; })()
    + (rows || `<p class="hint2">No payload files in that folder.</p>`);
}
async function autoScan() {
  const out = $("#autoStatus"); out.className = "result"; out.textContent = "Reading the payloads on the PS5 and checking for newer versions…";
  $("#autoScan").disabled = true;
  try { AUTO = await api("autoload/scan", {}); out.textContent = ""; } catch (e) { out.className = "result bad"; out.textContent = e.message; }
  $("#autoScan").disabled = false; renderAuto();
}
$("#autoScan").onclick = autoScan;
$("#autoList").addEventListener("click", async e => {
  if (e.target.closest("[data-automgr]")) { act("open", { path: AUTO.managerUrl }); return; }
  if (e.target.closest("#autoDirSave")) { await act("autoload/setdir", { Dir: $("#autoDir").value }); await poll(true); return autoScan(); }
  if (e.target.closest("#autoChange")) { const r = await modal(`<h3>Autoloader folder</h3><p>The folder on the PS5 that holds the payloads your jailbreak loads. Leave it empty to let Loadout look for it.</p><input value="${esc(S.settings.AutoDir || AUTO.dir || "")}" spellcheck="false">`, [{ label: "Cancel" }, { label: "Save", cls: "primary" }]); if (r && r.i === 1) { await act("autoload/setdir", { Dir: r.value }); await poll(true); autoScan(); } return; }
  if (e.target.closest("#autoCopy")) { navigator.clipboard.writeText((AUTO.seen || []).join("\n")).then(() => toast("Copied.")); return; }
  if (e.target.closest("#autoAll")) {
    const nvv = s => (s || "").toLowerCase().replace(/^v/, "");
    const todo = AUTO.items.filter(it => it.asset && !it.missing && !(it.same || (it.version && it.latest && nvv(it.version) === nvv(it.latest))));
    if (!todo.length || !(await confirmBox(`Update ${todo.length} payload${todo.length > 1 ? "s" : ""}?`, `Loadout will download the newest ${todo.map(x => "<b>" + esc(x.name || x.file) + "</b>").join(", ")} and put ${todo.length > 1 ? "them" : "it"} on the PS5. The current files are kept as backups. New versions take effect the next time you run the jailbreak.`, "Update all"))) return;
    let ok = 0; const bad = [];
    for (const it of todo) { $("#autoStatus").textContent = `Updating ${it.name || it.file}…`; try { await api("autoload/update", { File: it.file, Path: it.path }); ok++; } catch (err) { bad.push(`${it.file}: ${err.message}`); } }
    $("#autoStatus").textContent = "";
    toast(bad.length ? `Updated ${ok}. Not updated: ${bad.join("; ")}` : `Updated ${ok} payload${ok > 1 ? "s" : ""}.`, !!bad.length);
    autoScan(); return;
  }
  const t = e.target.closest("[data-autoupd]"); if (!t) return;
  const it = AUTO.items.find(x => x.file === t.dataset.autoupd && (!t.dataset.path || x.path === t.dataset.path)); if (!it) return;
  const from = it.via === "catalog" ? "Payload Manager's catalog" : esc(it.repo) + " on GitHub";
  const keep = AUTO.manager ? "The current file is saved on this PC first (in Loadout's PayloadBackups folder). The new one goes in under its new file name, and Payload Manager's version info and autoload list are updated to match." : `The current file is kept beside it as <span class="mono">${esc(it.file)}.old</span>.`;
  if (!(await confirmBox("Update this payload?", `Loadout will download <b>${esc(it.file)}</b> ${esc(it.latest)} from ${from} and put it on the PS5. ${keep} The new version takes effect the next time you run the jailbreak.`, "Update"))) return;
  t.disabled = true; t.textContent = "Updating…";
  try { const r = await api("autoload/update", { File: it.file, Path: it.path }) || {}; toast(`${it.name || it.file} updated to ${it.latest}${r.was && r.file !== r.was ? ", saved as " + r.file : ""}.${r.autoload ? " Payload Manager's autoload list now points at the new file." : ""}`); } catch (err) { toast(err.message, true); }
  autoScan();
});

// copy buttons that can sit anywhere, including inside a dialog
document.addEventListener("click", e => { const c = e.target.closest("[data-copytxt]"); if (c) { e.stopPropagation(); act("copy", { text: c.dataset.copytxt }, "Copied " + c.dataset.copytxt); } }, true);

// ---- home screen tiles for emulator games ----
let fwdForm = { icon: "", bg: "", music: "" };
document.addEventListener("click", async e => {
  const b = e.target.closest("[data-fwdpick]"); if (!b) return;
  e.stopPropagation();
  const kind = b.dataset.fwdpick;
  try {
    const r = await api("fwd/pick", { Kind: kind === "music" ? "music" : "image" });
    if (r.path) { fwdForm[kind] = r.path; const lab = document.querySelector(`[data-fwdlab="${kind}"]`); if (lab) lab.textContent = r.name; }
  } catch (err) { toast(err.message, true); }
}, true);
async function fwdMake(app, g, title) {
  if (!FWD.template) {
    const ok = await confirmBox("Set up home screen tiles", `A home screen tile needs a small launcher program made by the author of <span class="mono">prospero-eden-forwarder.mph.am</span>. Loadout doesn't include it, so it borrows it from a forwarder you made there.<br><br>Make one forwarder on that site for any game (once), then pick its <b>Forwarder.zip</b> here. Loadout keeps the launcher from it and uses it for every game from now on.`, "Pick the Forwarder.zip");
    if (!ok) return;
    try { const r = await api("fwd/template", {}); if (!r.picked) return; FWD.template = true; toast("Launcher saved. You only need to do that once."); } catch (err) { toast(err.message, true); return; }
  }
  fwdForm = { icon: "", bg: "", music: "" };
  const r = await modal(`<h3>Show on the PS5 home screen</h3>
    <p>Loadout makes a tile for this game and sends it to the PS5. Starting the tile opens ${esc(app.name)} straight into the game.</p>
    <div class="fwdform">
      <label>Name on the tile<input value="${esc(title)}" maxlength="80" spellcheck="false"></label>
      <div class="fwdrow"><span>Tile picture</span><b data-fwdlab="icon">${g.cover ? "The emulator's cover" : "A plain tile (no cover saved yet)"}</b><button class="btn ghost sm" data-fwdpick="icon">Pick a picture</button></div>
      <div class="fwdrow"><span>Backgrounds</span><b data-fwdlab="bg">Made from the tile picture (soft blur)</b><button class="btn ghost sm" data-fwdpick="bg">Pick a picture</button></div>
      <div class="fwdrow"><span>Music</span><b data-fwdlab="music">None</b><button class="btn ghost sm" data-fwdpick="music">Pick an .at9 file</button></div>
      <label class="switch"><input type="checkbox" id="fwdExit" checked><span></span>Close ${esc(app.name)} when I quit the game</label>
    </div>
    <p class="hint2">Needs on the PS5: the "forwardable" ProsperoEden (v1.000.070 or newer), <span class="mono">ps5-app-launcher.elf</span> loaded with your payloads, and ShadowMountPlus, which adds the tile the next time it looks at the homebrew folder.</p>`,
    [{ label: "Cancel" }, { label: "Make the tile", cls: "primary" }]);
  if (!r || r.i !== 1) return;
  const name = (r.value || title).trim(), exit = !!($("#fwdExit") || {}).checked;
  try {
    await api("fwd/make", { Target: app.folder, Rom: g.name, Title: name, Cover: g.cover || "", Icon: fwdForm.icon, BG: fwdForm.bg, Music: fwdForm.music, Exit: exit });
    toast(`Making the tile for ${name} and sending it to the PS5.`); poll(true); setTimeout(fwdLoad, 8000);
  } catch (err) { toast(err.message, true); }
}
async function fwdRemove(fw, title) {
  if (!await confirmBox("Remove from the home screen?", `This deletes the tile's small folder (<span class="mono">/data/homebrew/${esc(fw.id)}</span>) from the PS5. The game itself stays in the emulator.<br><br>If the tile lingers on the home screen afterwards, hide it from the Home screen tab.`, "Remove the tile", "danger solid")) return;
  try { await api("fwd/remove", { ID: fw.id }); toast(`Removed the tile for ${title}.`); fwdLoad(); } catch (err) { toast(err.message, true); }
}

// ---- cheats for emulator games ----
async function cheatsAdd(dir) {
  let r;
  try { r = await api("emu/cheats/pick", { Dir: dir }); } catch (err) { return toast(err.message, true); }
  if (!r || !r.path) return;
  if (!r.files) return toast(r.skipped ? `Found ${r.skipped} cheat file(s) in that folder, but none sit under a 16-character game ID folder, so Loadout can't tell which game they're for.` : "No cheat files found in that folder. Loadout looks for .txt files named by build ID, under folders named by the game's 16-character ID.", true);
  const some = r.mine > 0 && r.mine < r.titles;
  const btns = [{ label: "Cancel" }, { label: `Send everything (${r.titles} games)`, cls: some ? "ghost" : "primary" }];
  if (some) btns.push({ label: `Only my games (${r.mine})`, cls: "primary" });
  const big = r.files > 3000 ? `<br><br>That's a lot of small files. Sending all of them can take a long while; sending only your games is much quicker.` : "";
  const pick = await modal(`<h3>Add cheats</h3><p>Found cheats for <b>${r.titles}</b> game${r.titles === 1 ? "" : "s"} (${r.files} files) in that folder.${r.skipped ? ` ${r.skipped} file(s) had no game ID folder and are left out.` : ""}</p>
    <p>${some ? `<b>${r.mine}</b> of those match games Loadout can identify on your PS5 (from the ID in a game's file name, or a game folder the emulator already made under mods).` : r.mine ? "All of them match games on your PS5." : "Loadout couldn't match them to your games, because your game files don't have their ID in the name. Sending everything still works; the emulator only uses the ones for games you have."}${big}</p>
    <p>Loadout sorts them into the emulator's mods folder. Then on the PS5: Triangle on the game, <b>Mods</b>, and switch on the cheats you want.</p>`, btns);
  if (!pick || pick.i === 0) return;
  try { const s2 = await api("emu/cheats/send", { Dir: dir, Path: r.path, Only: pick.i === 2 }); toast(`Sending cheats for ${s2.titles} game${s2.titles === 1 ? "" : "s"} (${s2.files} files).`); poll(true); } catch (err) { toast(err.message, true); }
}

// ---- game IDs in emulator game names ----
async function gameIdAdd(dir, name, title) {
  let info;
  try { info = await api("emu/gameid", { Dir: dir, Name: name }); } catch (err) { return toast(err.message, true); }
  const chips = (info.candidates || []).filter(c => c !== info.guess).slice(0, 12).map(c => `<li data-pick="${esc(c)}" class="mono" style="cursor:pointer">${esc(c)}</li>`).join("");
  const r = await modal(`<h3>Game ID for ${esc(title)}</h3>
    <p>${info.guess ? `Loadout's answer is <b class="mono">${esc(info.guess)}</b> (${esc(info.why)}). Check it, then add it.` : "Loadout couldn't work this one out by itself. On the PS5, press Triangle on the game and open <b>Mods</b>: the screen names the game's folder, and that 16-character name is the ID."}</p>
    <input value="${esc(info.guess || "")}" maxlength="16" spellcheck="false" placeholder="0100000000000000" class="mono">
    ${chips ? `<p style="margin-top:10px">Game IDs on your PS5 that no game file has yet (click one to use it):</p><ul class="ftplist">${chips}</ul>` : ""}
    <p class="hint2">The file is renamed to end in <span class="mono">[the ID]</span>. Its saved cover and title are carried over.</p>`, [{ label: "Cancel" }, { label: "Add it to the name", cls: "primary" }]);
  const id = r && (r.pick || (r.i === 1 && r.value)); if (!id) return;
  try { const out = await api("emu/addid", { Dir: dir, Name: name, ID: id.trim() }); toast(`Renamed to ${out.name}.`); emuGames(); } catch (err) { toast(err.message, true); }
}
async function gameIdAll(dir) {
  if (!await confirmBox("Add game IDs to the names?", "Loadout adds each game's 16-character ID to its file name, like <span class=\"mono\">Game [0100ABCDEF012345].nsp</span>, for every game it can work out by itself. That makes cheats, mods and updates match up. Saved covers and titles are carried over.<br><br>Games it can't work out are left alone; add those one at a time from their Details.", "Add the IDs")) return;
  try {
    const r = await api("emu/addids", { Dir: dir });
    toast(`Added the ID to ${r.done} game${r.done === 1 ? "" : "s"}.${r.already ? ` ${r.already} already had one.` : ""}${(r.left || []).length ? ` ${r.left.length} need it added by hand, from Details.` : ""}`);
    emuGames();
  } catch (err) { toast(err.message, true); }
}

// ---- drag files from Explorer onto an emulator's page ----
// Without this, a file dropped anywhere on the window would replace the app with that file.
["dragover", "drop"].forEach(ev => window.addEventListener(ev, e => { if (e.dataTransfer && [...e.dataTransfer.types].includes("Files")) e.preventDefault(); }));
function emuDropTarget() {
  if (view !== "emu" || !EM.cur) return null;
  const app = EM.apps.find(a => a.path === EM.cur), sl = app && emuSlot(app);
  return sl && sl.dir ? { app, sl } : null;
}
let dropDepth = 0;
function dropHint(on) { $("#view-emu").classList.toggle("dropping", on); }
window.addEventListener("dragenter", e => { if (!e.dataTransfer || ![...e.dataTransfer.types].includes("Files") || !emuDropTarget()) return; dropDepth++; dropHint(true); });
window.addEventListener("dragleave", () => { dropDepth = Math.max(0, dropDepth - 1); if (!dropDepth) dropHint(false); });
window.addEventListener("drop", async e => {
  dropDepth = 0; dropHint(false);
  const t = emuDropTarget(); if (!t || !e.dataTransfer || !e.dataTransfer.files.length) return;
  if (t.sl.key === "mods") return toast("Cheats come in as a folder: use Add a cheats folder.", true);
  const files = [], skipped = [];
  [...e.dataTransfer.items].forEach((it, i) => {
    const ent = it.webkitGetAsEntry && it.webkitGetAsEntry(), f = e.dataTransfer.files[i];
    if (!f) return;
    if (ent && ent.isDirectory) skipped.push(f.name); else files.push(f);
  });
  if (skipped.length) toast(`Folders can't be dropped (${skipped.join(", ")}). Use Add a folder for those.`, true);
  if (!files.length) return;
  toast(`Adding ${files.length} file${files.length > 1 ? "s" : ""} to ${t.app.name}. Keep Loadout open until ${files.length > 1 ? "they finish" : "it finishes"}.`);
  for (const f of files) {
    try {
      const u = `/api/emu/drop?dir=${encodeURIComponent(t.sl.dir)}&name=${encodeURIComponent(f.name)}&size=${f.size}&unzip=${t.sl.unzip ? 1 : 0}`;
      const kick = setTimeout(() => poll(true), 600);
      const r = await fetch(u, { method: "POST", headers: { "X-Token": token }, body: f });
      clearTimeout(kick);
      const j = await r.json(); if (j.error) throw new Error(j.error);
    } catch (err) { toast(`${f.name}: ${err.message}`, true); }
    poll(true);
  }
  emuGames();
});
