// liveurl dashboard client. Hard rule throughout this file: webhook event
// data (method/path/query/body) comes from arbitrary internet senders —
// that's the product's whole point — so it is rendered exclusively via
// textContent (through the el()/td() helpers below), never innerHTML.
// innerHTML is only ever set to a literal '' to clear a container.
const API_BASE = '/dashboard/api';
const TOKEN_KEY = 'liveurl_token';

let token = localStorage.getItem(TOKEN_KEY);
let currentTunnel = null;
let pollHandle = null;

const loginView = document.getElementById('login-view');
const appView = document.getElementById('app-view');
const logoutBtn = document.getElementById('logout-btn');
const tokenInput = document.getElementById('token-input');
const connectBtn = document.getElementById('connect-btn');
const loginError = document.getElementById('login-error');
const tunnelsList = document.getElementById('tunnels-list');
const detailEmpty = document.getElementById('detail-empty');
const detailView = document.getElementById('detail-view');
const detailTitle = document.getElementById('detail-title');
const statState = document.getElementById('stat-state');
const statQueued = document.getElementById('stat-queued');
const statPages = document.getElementById('stat-pages');
const statBytes = document.getElementById('stat-bytes');
const eventsTbody = document.getElementById('events-tbody');
const eventsEmpty = document.getElementById('events-empty');
const clearEventsBtn = document.getElementById('clear-events-btn');

// Mirrors the {"error": "..."} shape used throughout internal/control and
// already unwrapped the same way by cmd/liveurl/api.go's CLI client.
async function api(path, opts) {
  opts = opts || {};
  const resp = await fetch(API_BASE + path, {
    ...opts,
    headers: { ...(opts.headers || {}), Authorization: 'Bearer ' + token },
  });
  const text = await resp.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch (e) { body = null; }
  }
  if (!resp.ok) {
    const msg = (body && body.error) || ('request failed with status ' + resp.status);
    throw new Error(msg);
  }
  return body;
}

function el(tag, opts) {
  opts = opts || {};
  const e = document.createElement(tag);
  if (opts.className) e.className = opts.className;
  if (opts.text !== undefined) e.textContent = opts.text;
  if (opts.onClick) e.addEventListener('click', opts.onClick);
  return e;
}

function td(value) {
  const cell = document.createElement('td');
  cell.textContent = String(value);
  return cell;
}

function tdCode(value) {
  const cell = document.createElement('td');
  const code = document.createElement('code');
  code.textContent = value;
  cell.appendChild(code);
  return cell;
}

function tdBadge(state) {
  const cell = document.createElement('td');
  const badge = document.createElement('span');
  badge.className = 'badge badge-' + state;
  badge.textContent = state;
  cell.appendChild(badge);
  return cell;
}

function formatBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / (1024 * 1024)).toFixed(1) + ' MB';
}

function showApp() {
  loginView.classList.add('hidden');
  appView.classList.remove('hidden');
  logoutBtn.classList.remove('hidden');
}

function showLogin(errMsg) {
  loginView.classList.remove('hidden');
  appView.classList.add('hidden');
  logoutBtn.classList.add('hidden');
  loginError.textContent = errMsg || '';
  if (pollHandle) {
    clearInterval(pollHandle);
    pollHandle = null;
  }
}

async function loadTunnels() {
  let tunnels;
  try {
    tunnels = await api('/tunnels');
  } catch (err) {
    showLogin('Could not load tunnels: ' + err.message);
    return;
  }
  tunnelsList.innerHTML = '';
  if (!tunnels || tunnels.length === 0) {
    tunnelsList.appendChild(el('div', { className: 'empty', text: 'No tunnels yet — run `liveurl http <port>` to create one.' }));
    return;
  }
  for (const t of tunnels) {
    const row = el('div', { className: 'tunnel-row' + (t.subdomain === currentTunnel ? ' active' : '') });
    row.appendChild(el('span', { className: 'tunnel-name', text: t.subdomain }));
    row.appendChild(el('span', {
      className: 'badge ' + (t.online ? 'badge-online' : 'badge-offline'),
      text: t.online ? 'Online' : 'Offline',
    }));
    row.addEventListener('click', () => selectTunnel(t.subdomain));
    tunnelsList.appendChild(row);
  }
}

function selectTunnel(sub) {
  currentTunnel = sub;
  detailEmpty.classList.add('hidden');
  detailView.classList.remove('hidden');
  detailTitle.textContent = sub;
  loadTunnels();
  loadDetail();
}

async function loadDetail() {
  if (!currentTunnel) return;

  try {
    const status = await api('/status?tunnel=' + encodeURIComponent(currentTunnel));
    statState.textContent = status.online ? 'Online' : 'Offline';
    statQueued.textContent = status.queued_events;
    statPages.textContent = status.snapshot_pages;
    statBytes.textContent = formatBytes(status.snapshot_bytes);
  } catch (err) {
    return; // token invalidated or tunnel gone mid-poll; skip this tick
  }

  try {
    const events = await api('/events?tunnel=' + encodeURIComponent(currentTunnel));
    renderEvents(events || []);
  } catch (err) {
    // ignore this tick
  }
}

function renderEvents(events) {
  eventsTbody.innerHTML = '';
  if (events.length === 0) {
    eventsEmpty.classList.remove('hidden');
    return;
  }
  eventsEmpty.classList.add('hidden');
  for (const e of events) {
    const tr = document.createElement('tr');
    tr.appendChild(td(e.ID));
    tr.appendChild(td(e.Method));
    tr.appendChild(tdCode(e.Path + (e.Query ? '?' + e.Query : '')));
    tr.appendChild(tdBadge(e.State));
    tr.appendChild(td(e.Attempts));
    tr.appendChild(td(new Date(e.ReceivedAt).toLocaleString()));

    const actionsTd = document.createElement('td');
    const replayBtn = el('button', { className: 'btn btn-secondary btn-sm', text: 'Replay' });
    replayBtn.addEventListener('click', () => replayEvent(e.ID));
    actionsTd.appendChild(replayBtn);
    tr.appendChild(actionsTd);

    eventsTbody.appendChild(tr);
  }
}

async function replayEvent(id) {
  try {
    await api('/events/' + id + '/replay', { method: 'POST' });
  } catch (err) {
    alert('Replay failed: ' + err.message);
  }
  loadDetail();
}

clearEventsBtn.addEventListener('click', async () => {
  if (!currentTunnel) return;
  if (!confirm('Clear all buffered events for ' + currentTunnel + '?')) return;
  try {
    await api('/events?tunnel=' + encodeURIComponent(currentTunnel), { method: 'DELETE' });
  } catch (err) {
    alert('Clear failed: ' + err.message);
  }
  loadDetail();
});

connectBtn.addEventListener('click', async () => {
  const val = tokenInput.value.trim();
  if (!val) return;
  token = val;
  try {
    await api('/tunnels');
  } catch (err) {
    token = null;
    loginError.textContent = 'Could not connect: ' + err.message;
    return;
  }
  localStorage.setItem(TOKEN_KEY, val);
  loginError.textContent = '';
  start();
});

logoutBtn.addEventListener('click', () => {
  localStorage.removeItem(TOKEN_KEY);
  token = null;
  currentTunnel = null;
  showLogin();
});

function start() {
  showApp();
  loadTunnels();
  loadDetail();
  if (pollHandle) clearInterval(pollHandle);
  pollHandle = setInterval(() => {
    loadTunnels();
    loadDetail();
  }, 4000);
}

if (token) {
  start();
} else {
  showLogin();
}
