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
const loginError = document.getElementById('login-error');
const tunnelsList = document.getElementById('tunnels-list');

const tabSignup = document.getElementById('tab-signup');
const tabLogin = document.getElementById('tab-login');
const tabToken = document.getElementById('tab-token');
const signupPanel = document.getElementById('signup-panel');
const loginPanel = document.getElementById('login-panel');
const tokenPanel = document.getElementById('token-panel');
const signupEmail = document.getElementById('signup-email');
const signupPassword = document.getElementById('signup-password');
const signupError = document.getElementById('signup-error');
const loginEmail = document.getElementById('login-email');
const loginPassword = document.getElementById('login-password');
const loginPanelError = document.getElementById('login-panel-error');
const tokenReveal = document.getElementById('token-reveal');
const tokenRevealValue = document.getElementById('token-reveal-value');
const tokenCopyBtn = document.getElementById('token-copy-btn');
const tokenContinueBtn = document.getElementById('token-continue-btn');
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

// apiPublic hits the unauthenticated /api/signup and /api/login endpoints —
// no Bearer header, since there's no token yet.
async function apiPublic(path, body) {
  const resp = await fetch(API_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const text = await resp.text();
  let parsed = null;
  if (text) {
    try { parsed = JSON.parse(text); } catch (e) { parsed = null; }
  }
  if (!resp.ok) {
    const msg = (parsed && parsed.error) || ('request failed with status ' + resp.status);
    throw new Error(msg);
  }
  return parsed;
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

const authTabs = { signup: [tabSignup, signupPanel], login: [tabLogin, loginPanel], token: [tabToken, tokenPanel] };

function showAuthTab(name) {
  tokenReveal.classList.add('hidden');
  for (const key in authTabs) {
    const [tabBtn, panel] = authTabs[key];
    tabBtn.classList.toggle('active', key === name);
    panel.classList.toggle('hidden', key !== name);
  }
}

tabSignup.addEventListener('click', () => showAuthTab('signup'));
tabLogin.addEventListener('click', () => showAuthTab('login'));
tabToken.addEventListener('click', () => showAuthTab('token'));

// revealToken shows a freshly-minted plaintext token once (only auth_tokens'
// hash is ever persisted server-side, so this is the only chance to see
// it) and stashes it so "Continue to dashboard" can log straight in.
let pendingToken = null;
function revealToken(value) {
  pendingToken = value;
  tokenRevealValue.textContent = value;
  signupPanel.classList.add('hidden');
  loginPanel.classList.add('hidden');
  tokenPanel.classList.add('hidden');
  tokenReveal.classList.remove('hidden');
}

tokenCopyBtn.addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(pendingToken || '');
    tokenCopyBtn.textContent = 'Copied';
    setTimeout(() => { tokenCopyBtn.textContent = 'Copy token'; }, 1500);
  } catch (err) {
    // clipboard API unavailable (e.g. insecure context) — the token is
    // still selectable as plain text in the box above.
  }
});

tokenContinueBtn.addEventListener('click', () => {
  if (!pendingToken) return;
  token = pendingToken;
  pendingToken = null;
  localStorage.setItem(TOKEN_KEY, token);
  start();
});

signupPanel.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  signupError.textContent = '';
  const email = signupEmail.value.trim();
  const password = signupPassword.value;
  if (!email || !password) return;
  try {
    const result = await apiPublic('/signup', { email, password });
    revealToken(result.token);
  } catch (err) {
    signupError.textContent = err.message;
  }
});

loginPanel.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  loginPanelError.textContent = '';
  const email = loginEmail.value.trim();
  const password = loginPassword.value;
  if (!email || !password) return;
  try {
    const result = await apiPublic('/login', { email, password });
    revealToken(result.token);
  } catch (err) {
    loginPanelError.textContent = err.message;
  }
});

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

tokenPanel.addEventListener('submit', async (ev) => {
  ev.preventDefault();
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
  showAuthTab('signup');
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
