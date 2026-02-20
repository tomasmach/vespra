'use strict';

// --- View switching ---

const VIEWS = ['config', 'memories', 'monitor'];

function showView(name) {
  VIEWS.forEach(v => {
    document.getElementById('view-' + v).hidden = (v !== name);
    document.getElementById('nav-' + v).classList.toggle('active', v === name);
  });
  if (name === 'config' && !configLoaded) loadConfig();
}

// --- Config tab ---

let configLoaded = false;

function loadConfig() {
  fetch('/api/config')
    .then(r => r.text())
    .then(text => {
      document.getElementById('config-editor').value = text;
      configLoaded = true;
    })
    .catch(() => setConfigStatus('Failed to load config', true));
}

function saveConfig() {
  fetch('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: document.getElementById('config-editor').value,
  })
    .then(r => {
      if (r.ok) setConfigStatus('Saved.', false);
      else r.text().then(t => setConfigStatus(t || 'Save failed', true));
    })
    .catch(() => setConfigStatus('Save failed', true));
}

function setConfigStatus(msg, isError) {
  const el = document.getElementById('config-status');
  el.textContent = msg;
  el.className = 'status-msg' + (isError ? ' error' : '');
  if (!isError) setTimeout(() => { el.textContent = ''; }, 3000);
}

// --- Memories tab ---

function loadMemories() {
  const serverID = document.getElementById('mem-server').value.trim();
  const statusEl = document.getElementById('mem-status');
  if (!serverID) {
    statusEl.textContent = 'Server ID is required.';
    statusEl.className = 'status-msg error';
    return;
  }
  statusEl.textContent = '';
  statusEl.className = 'status-msg';

  const userID = document.getElementById('mem-user').value.trim();
  const q = document.getElementById('mem-query').value.trim();
  const params = new URLSearchParams({ server_id: serverID, limit: 25, offset: 0 });
  if (userID) params.set('user_id', userID);
  if (q) params.set('q', q);

  fetch('/api/memories?' + params)
    .then(r => r.json())
    .then(data => renderMemories(data, serverID))
    .catch(() => {
      statusEl.textContent = 'Failed to load memories.';
      statusEl.className = 'status-msg error';
    });
}

function renderMemories(data, serverID) {
  const countEl = document.getElementById('mem-count');
  const grid = document.getElementById('mem-grid');
  const total = data.total || 0;
  countEl.textContent = total + ' result' + (total !== 1 ? 's' : '');
  countEl.hidden = false;
  grid.innerHTML = '';

  (data.memories || []).forEach(m => {
    const created = m.CreatedAt ? new Date(m.CreatedAt).toLocaleString() : '';
    const card = document.createElement('div');
    card.className = 'memory-card';
    card.innerHTML =
      '<div class="memory-content">' + esc(m.Content) + '</div>' +
      '<div class="memory-footer">' +
        '<div class="memory-meta">' +
          '<span class="meta-id">' + esc(m.ServerID) + (m.UserID ? ' · ' + esc(m.UserID) : '') + '</span>' +
          '<span class="meta-time">' + esc(created) + '</span>' +
        '</div>' +
        '<div class="memory-actions">' +
          '<button class="btn-edit" onclick="openEditModal(' + JSON.stringify(m.ID) + ',' + JSON.stringify(m.Content) + ',' + JSON.stringify(serverID) + ')">Edit</button>' +
          '<button class="btn-danger" onclick="deleteMemory(' + JSON.stringify(m.ID) + ',' + JSON.stringify(serverID) + ')">Delete</button>' +
        '</div>' +
      '</div>';
    grid.appendChild(card);
  });
}

function deleteMemory(id, serverID) {
  if (!confirm('Delete this memory?')) return;
  fetch('/api/memories/' + encodeURIComponent(id) + '?server_id=' + encodeURIComponent(serverID), { method: 'DELETE' })
    .then(r => { if (r.ok) loadMemories(); else r.text().then(t => alert('Delete failed: ' + t)); })
    .catch(() => alert('Delete failed.'));
}

// --- Edit modal ---

let editModalID = null;
let editModalServerID = null;

function openEditModal(id, content, serverID) {
  editModalID = id;
  editModalServerID = serverID;
  document.getElementById('edit-textarea').value = content;
  document.getElementById('edit-modal').showModal();
}

function closeEditModal() {
  document.getElementById('edit-modal').close();
  editModalID = null;
  editModalServerID = null;
}

function confirmEdit() {
  const newContent = document.getElementById('edit-textarea').value;
  if (editModalID === null) return;
  fetch('/api/memories/' + encodeURIComponent(editModalID) + '?server_id=' + encodeURIComponent(editModalServerID), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: newContent }),
  })
    .then(r => {
      if (r.ok) { closeEditModal(); loadMemories(); }
      else r.text().then(t => alert('Edit failed: ' + t));
    })
    .catch(() => alert('Edit failed.'));
}

// --- Monitor tab (SSE) ---

let sseConn = null;

function connectSSE() {
  if (sseConn) sseConn.close();
  sseConn = new EventSource('/api/events');

  sseConn.addEventListener('status', e => {
    try {
      const agents = JSON.parse(e.data);
      renderAgents(agents);
    } catch (_) {}
  });

  sseConn.addEventListener('config_reloaded', () => {
    setConfigStatus('Config reloaded!', false);
    configLoaded = false;
  });

  sseConn.onerror = () => {
    setSseStatus(false);
    document.getElementById('monitor-status').textContent = 'Reconnecting…';
  };

  sseConn.onopen = () => {
    setSseStatus(true);
  };
}

function setSseStatus(connected) {
  const dot = document.getElementById('sse-dot');
  const label = document.getElementById('sse-label');
  if (connected) {
    dot.className = 'sse-dot connected';
    label.textContent = 'Live';
  } else {
    dot.className = 'sse-dot error';
    label.textContent = 'Reconnecting…';
  }
}

function renderAgents(agents) {
  const grid = document.getElementById('agent-grid');
  const statusEl = document.getElementById('monitor-status');
  const dot = document.getElementById('agent-count-dot');
  grid.innerHTML = '';

  if (!agents || agents.length === 0) {
    statusEl.textContent = 'No active agents.';
    dot.className = 'status-dot';
    return;
  }
  statusEl.textContent = agents.length + ' active agent' + (agents.length !== 1 ? 's' : '');
  dot.className = 'status-dot active';

  agents.forEach(a => {
    const last = a.last_active ? new Date(a.last_active).toLocaleString() : '—';
    const qd = a.queue_depth || 0;
    const badgeClass = qd === 0 ? 'badge badge-green' : qd <= 2 ? 'badge badge-amber' : 'badge badge-red';
    const card = document.createElement('div');
    card.className = 'agent-card';
    card.innerHTML =
      '<div class="agent-channel">' + esc(a.channel_id) + '</div>' +
      '<div class="agent-server">' + esc(a.server_id) + '</div>' +
      '<div class="agent-footer">' +
        '<span class="agent-time">' + esc(last) + '</span>' +
        '<div class="agent-status">' +
          '<span class="status-dot' + (qd > 0 ? ' active' : '') + '"></span>' +
          '<span class="' + badgeClass + '">' + esc(String(qd)) + '</span>' +
        '</div>' +
      '</div>';
    grid.appendChild(card);
  });
}

// --- Utility ---

function esc(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// --- Init ---

showView('config');
connectSSE();
