'use strict';

// --- View switching ---

const VIEWS = ['config', 'agents', 'memories', 'monitor'];

function showView(name) {
  VIEWS.forEach(v => {
    document.getElementById('view-' + v).hidden = (v !== name);
    document.getElementById('nav-' + v).classList.toggle('active', v === name);
  });
  if (name === 'config' && !configLoaded) loadConfig();
  if (name === 'agents') loadAgents();
  if (name === 'memories') loadAgentList(); // populate dropdown
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

// --- Agents tab ---

let editingAgentID = null;

function loadAgents() {
  fetch('/api/agents')
    .then(r => r.json())
    .then(renderAgentList)
    .catch(() => setAgentsStatus('Failed to load agents', true));
}

function renderAgentList(agents) {
  const list = document.getElementById('agents-list');
  list.innerHTML = '';
  if (!agents || agents.length === 0) {
    list.innerHTML = '<p class="empty-msg">No agents configured.</p>';
    return;
  }
  agents.forEach(a => {
    const row = document.createElement('div');
    row.className = 'agent-row';
    row.innerHTML =
      '<div class="agent-row-info">' +
        '<strong>' + esc(a.id) + '</strong>' +
        ' <span class="meta-id">' + esc(a.server_id) + '</span>' +
        (a.has_token ? ' <span class="badge badge-green">custom token</span>' : '') +
        (a.soul_file ? ' <span class="meta-id">' + esc(a.soul_file) + '</span>' : '') +
        (a.response_mode ? ' <span class="badge badge-amber">' + esc(a.response_mode) + '</span>' : '') +
      '</div>' +
      '<div class="agent-row-actions">' +
        '<button class="btn-edit" onclick="editAgent(' + JSON.stringify(a) + ')">Edit</button>' +
        '<button class="btn-danger" onclick="deleteAgent(' + JSON.stringify(a.id) + ')">Delete</button>' +
      '</div>';
    list.appendChild(row);
  });
}

function openNewAgentForm() {
  editingAgentID = null;
  document.getElementById('agent-form-title').textContent = 'New Agent';
  document.getElementById('af-id').value = '';
  document.getElementById('af-id').disabled = false;
  document.getElementById('af-server-id').value = '';
  document.getElementById('af-token').value = '';
  document.getElementById('af-soul-file').value = '';
  document.getElementById('af-db-path').value = '';
  document.getElementById('af-response-mode').value = '';
  document.getElementById('agent-form-card').hidden = false;
}

function editAgent(a) {
  editingAgentID = a.id;
  document.getElementById('agent-form-title').textContent = 'Edit Agent';
  document.getElementById('af-id').value = a.id;
  document.getElementById('af-id').disabled = true;
  document.getElementById('af-server-id').value = a.server_id;
  document.getElementById('af-token').value = ''; // never pre-fill token
  document.getElementById('af-soul-file').value = a.soul_file || '';
  document.getElementById('af-db-path').value = a.db_path || '';
  document.getElementById('af-response-mode').value = a.response_mode || '';
  document.getElementById('agent-form-card').hidden = false;
}

function closeAgentForm() {
  document.getElementById('agent-form-card').hidden = true;
  editingAgentID = null;
}

function saveAgent() {
  const id = document.getElementById('af-id').value.trim();
  const serverID = document.getElementById('af-server-id').value.trim();
  if (!id) { setAgentsStatus('ID is required', true); return; }
  if (!serverID) { setAgentsStatus('Server ID is required', true); return; }

  const body = {
    id: id,
    server_id: serverID,
    token: document.getElementById('af-token').value,
    soul_file: document.getElementById('af-soul-file').value.trim(),
    db_path: document.getElementById('af-db-path').value.trim(),
    response_mode: document.getElementById('af-response-mode').value,
  };

  const isEdit = editingAgentID !== null;
  const url = isEdit ? '/api/agents/' + encodeURIComponent(editingAgentID) : '/api/agents';
  const method = isEdit ? 'PUT' : 'POST';

  fetch(url, {
    method: method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(r => {
      if (r.ok) {
        closeAgentForm();
        loadAgents();
        setAgentsStatus(isEdit ? 'Agent updated.' : 'Agent created.', false);
      } else {
        r.text().then(t => setAgentsStatus(t || 'Save failed', true));
      }
    })
    .catch(() => setAgentsStatus('Save failed', true));
}

function deleteAgent(id) {
  if (!confirm('Delete agent "' + id + '"? The memory DB will not be deleted.')) return;
  fetch('/api/agents/' + encodeURIComponent(id), { method: 'DELETE' })
    .then(r => {
      if (r.ok) loadAgents();
      else r.text().then(t => alert('Delete failed: ' + t));
    })
    .catch(() => alert('Delete failed.'));
}

function setAgentsStatus(msg, isError) {
  const el = document.getElementById('agents-status');
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

// populate agent dropdown for memories tab
function loadAgentList() {
  fetch('/api/agents')
    .then(r => r.json())
    .then(agents => {
      const sel = document.getElementById('mem-agent-select');
      // keep the first "— select agent —" option
      sel.options.length = 1;
      (agents || []).forEach(a => {
        const opt = document.createElement('option');
        opt.value = a.server_id;
        opt.textContent = a.id + ' (' + a.server_id + ')';
        sel.appendChild(opt);
      });
    })
    .catch(() => {});
}

function onMemAgentChange() {
  const sel = document.getElementById('mem-agent-select');
  const serverInput = document.getElementById('mem-server');
  if (sel.value) {
    serverInput.value = sel.value;
  }
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
