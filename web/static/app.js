'use strict';

// --- State ---

let agents = [];
let selectedAgentId = null;
let currentMemServerID = null;

// --- Init ---

function init() {
  loadAgents().then(() => router());
  connectSSE();
  window.addEventListener('hashchange', router);
  document.getElementById('btn-settings').addEventListener('click', () => navigate('#/config'));
  document.getElementById('btn-new-agent').addEventListener('click', () => navigate('#/agents/new'));
}

// --- Router ---

function navigate(hash) {
  location.hash = hash;
}

function navigateTab(tab) {
  if (!selectedAgentId) return;
  navigate(tab === 'config'
    ? '#/agents/' + encodeURIComponent(selectedAgentId)
    : '#/agents/' + encodeURIComponent(selectedAgentId) + '/' + tab);
}

function navigateConfigTab(tab) {
  navigate(tab === 'soul' ? '#/config/soul' : '#/config');
}

function renderConfigTab(tab) {
  ['config', 'soul'].forEach(t => {
    document.getElementById('ctab-' + t).classList.toggle('active', t === tab);
    document.getElementById('global-' + t).hidden = (t !== tab);
  });
  if (tab === 'config') loadConfig();
  if (tab === 'soul') loadGlobalSoul();
}

function router() {
  const hash = location.hash;

  if (hash === '#/config' || hash === '#/config/soul') {
    selectedAgentId = null;
    renderAgentSidebar();
    showPanel('config');
    renderConfigTab(hash === '#/config/soul' ? 'soul' : 'config');
    return;
  }

  if (hash === '#/agents/new') {
    selectedAgentId = null;
    renderAgentSidebar();
    clearNewAgentForm();
    showPanel('new-agent');
    return;
  }

  const m = hash.match(/^#\/agents\/([^/]+?)(?:\/(config|soul|memories))?$/);
  if (m) {
    const id = decodeURIComponent(m[1]);
    const tab = m[2] || 'config';
    selectedAgentId = id;
    renderAgentSidebar();
    const agent = agents.find(a => a.id === id);
    if (!agent) { showPanel('empty'); return; }
    populateAgentPanel(agent);
    showPanel('agent');
    renderDetailTab(tab);
    return;
  }

  selectedAgentId = null;
  renderAgentSidebar();
  showPanel('empty');
}

// --- Agent sidebar ---

function loadAgents() {
  return fetch('/api/agents')
    .then(r => r.json())
    .then(data => {
      agents = data || [];
      renderAgentSidebar();
    })
    .catch(() => console.error('Failed to load agents'));
}

function renderAgentSidebar() {
  const list = document.getElementById('agent-sidebar-list');
  list.innerHTML = '';
  if (!agents.length) {
    list.innerHTML = '<p class="empty-msg" style="padding:0.75rem 0.5rem;font-size:0.8rem;">No agents configured yet.</p>';
    return;
  }
  agents.forEach(a => {
    const item = document.createElement('button');
    item.className = 'sidebar-agent-item' + (a.id === selectedAgentId ? ' active' : '');
    item.innerHTML =
      '<span class="agent-item-name">' + esc(a.id) + '</span>' +
      '<span class="agent-item-server">' + esc(a.server_id) + '</span>';
    item.addEventListener('click', () => navigate('#/agents/' + encodeURIComponent(a.id)));
    list.appendChild(item);
  });
}

function showPanel(name) {
  document.getElementById('panel-empty').hidden = name !== 'empty';
  document.getElementById('panel-config').hidden = name !== 'config';
  document.getElementById('panel-new-agent').hidden = name !== 'new-agent';
  document.getElementById('panel-agent').hidden = name !== 'agent';
  document.getElementById('btn-settings').classList.toggle('active', name === 'config');
}

// --- Agent detail ---

function populateAgentPanel(agent) {
  document.getElementById('agent-detail-name').textContent = agent.id;
  document.getElementById('cfg-server-id').value = agent.server_id || '';
  document.getElementById('cfg-token').value = '';
  document.getElementById('cfg-soul-file').value = agent.soul_file || '';
  document.getElementById('cfg-db-path').value = agent.db_path || '';
  document.getElementById('cfg-response-mode').value = agent.response_mode || '';
  document.getElementById('cfg-status').textContent = '';
}

function renderDetailTab(tab) {
  ['config', 'soul', 'memories'].forEach(t => {
    document.getElementById('dtab-' + t).classList.toggle('active', t === tab);
    document.getElementById('detail-' + t).hidden = (t !== tab);
  });

  if (tab === 'soul' && selectedAgentId) loadSoul(selectedAgentId);
  if (tab === 'memories' && selectedAgentId) {
    const agent = agents.find(a => a.id === selectedAgentId);
    if (agent) {
      currentMemServerID = agent.server_id;
      searchMemories();
    }
  }
}

// --- Config tab ---

function saveAgentConfig() {
  if (!selectedAgentId) return;
  const serverID = document.getElementById('cfg-server-id').value.trim();
  if (!serverID) { setCfgStatus('Server ID is required', true); return; }

  const body = {
    id: selectedAgentId,
    server_id: serverID,
    token: document.getElementById('cfg-token').value,
    soul_file: document.getElementById('cfg-soul-file').value.trim(),
    db_path: document.getElementById('cfg-db-path').value.trim(),
    response_mode: document.getElementById('cfg-response-mode').value,
  };

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(r => {
      if (r.ok) {
        setCfgStatus('Saved.', false);
        loadAgents();
      } else {
        r.text().then(t => setCfgStatus(t || 'Save failed', true));
      }
    })
    .catch(() => setCfgStatus('Save failed', true));
}

function setCfgStatus(msg, isError) {
  setStatus('cfg-status', msg, isError);
}

// --- Soul tab ---

function loadSoul(agentId) {
  document.getElementById('soul-editor').value = '';
  document.getElementById('soul-path-info').textContent = 'Loading…';
  document.getElementById('soul-status').textContent = '';

  fetch('/api/agents/' + encodeURIComponent(agentId) + '/soul')
    .then(r => r.json())
    .then(data => {
      document.getElementById('soul-editor').value = data.content || '';
      document.getElementById('soul-path-info').textContent = data.using_default
        ? 'No soul file configured — will be auto-created on save.'
        : (data.path || '');
    })
    .catch(() => {
      document.getElementById('soul-path-info').textContent = 'Failed to load soul.';
    });
}

function saveSoul() {
  if (!selectedAgentId) return;
  const content = document.getElementById('soul-editor').value;

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/soul', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
    .then(r => {
      if (r.ok) return r.json();
      return r.text().then(t => { throw new Error(t || 'Save failed'); });
    })
    .then(data => {
      setSoulStatus('Saved.', false);
      document.getElementById('soul-path-info').textContent = data.path;
      loadAgents();
    })
    .catch(e => setSoulStatus(e.message, true));
}

function setSoulStatus(msg, isError) {
  setStatus('soul-status', msg, isError);
}

// --- Global soul ---

function loadGlobalSoul() {
  document.getElementById('global-soul-editor').value = '';
  document.getElementById('global-soul-path-info').textContent = 'Loading…';
  document.getElementById('global-soul-status').textContent = '';

  fetch('/api/soul')
    .then(r => r.json())
    .then(data => {
      document.getElementById('global-soul-editor').value = data.content || '';
      document.getElementById('global-soul-path-info').textContent = data.using_default
        ? 'No soul file configured — will be auto-created on save.'
        : (data.path || '');
    })
    .catch(() => {
      document.getElementById('global-soul-path-info').textContent = 'Failed to load soul.';
    });
}

function saveGlobalSoul() {
  const content = document.getElementById('global-soul-editor').value;
  fetch('/api/soul', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
    .then(r => {
      if (r.ok) return r.json();
      return r.text().then(t => { throw new Error(t || 'Save failed'); });
    })
    .then(data => {
      setStatus('global-soul-status', 'Saved.', false);
      document.getElementById('global-soul-path-info').textContent = data.path;
    })
    .catch(e => setStatus('global-soul-status', e.message, true));
}

// --- Global config ---

function loadConfig() {
  document.getElementById('config-editor').value = '';
  document.getElementById('config-path-info').textContent = 'Loading…';
  document.getElementById('config-status').textContent = '';

  fetch('/api/config')
    .then(r => {
      if (!r.ok) throw new Error('Failed to load config');
      return r.text();
    })
    .then(text => {
      document.getElementById('config-editor').value = text;
      document.getElementById('config-path-info').textContent = 'Global config (TOML)';
    })
    .catch(() => {
      document.getElementById('config-path-info').textContent = 'Failed to load config.';
    });
}

function saveConfig() {
  const content = document.getElementById('config-editor').value;
  fetch('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: content,
  })
    .then(r => {
      if (r.ok) {
        setConfigStatus('Saved.', false);
      } else {
        r.text().then(t => setConfigStatus(t || 'Save failed', true));
      }
    })
    .catch(() => setConfigStatus('Save failed', true));
}

function setConfigStatus(msg, isError) {
  setStatus('config-status', msg, isError);
}

// --- New agent ---

function clearNewAgentForm() {
  document.getElementById('na-id').value = '';
  document.getElementById('na-server-id').value = '';
  document.getElementById('na-token').value = '';
  document.getElementById('na-soul-file').value = '';
  document.getElementById('na-db-path').value = '';
  document.getElementById('na-response-mode').value = '';
  document.getElementById('new-agent-status').textContent = '';
}

function cancelNewAgent() {
  navigate('');
}

function createAgent() {
  const id = document.getElementById('na-id').value.trim();
  const serverID = document.getElementById('na-server-id').value.trim();
  if (!id) { setNewAgentStatus('ID is required', true); return; }
  if (!serverID) { setNewAgentStatus('Server ID is required', true); return; }

  const body = {
    id,
    server_id: serverID,
    token: document.getElementById('na-token').value,
    soul_file: document.getElementById('na-soul-file').value.trim(),
    db_path: document.getElementById('na-db-path').value.trim(),
    response_mode: document.getElementById('na-response-mode').value,
  };

  fetch('/api/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(r => {
      if (r.ok) {
        loadAgents().then(() => navigate('#/agents/' + encodeURIComponent(id)));
      } else {
        r.text().then(t => setNewAgentStatus(t || 'Create failed', true));
      }
    })
    .catch(() => setNewAgentStatus('Create failed', true));
}

function setNewAgentStatus(msg, isError) {
  setStatus('new-agent-status', msg, isError);
}

function deleteSelectedAgent() {
  if (!selectedAgentId) return;
  if (!confirm('Delete agent "' + selectedAgentId + '"? The memory DB will not be deleted.')) return;

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId), { method: 'DELETE' })
    .then(r => {
      if (r.ok) {
        selectedAgentId = null;
        loadAgents();
        navigate('');
      } else {
        r.text().then(t => alert('Delete failed: ' + t));
      }
    })
    .catch(() => alert('Delete failed.'));
}

// --- Memories tab ---

function searchMemories() {
  if (!currentMemServerID) return;
  const statusEl = document.getElementById('mem-status');
  statusEl.textContent = '';

  const userID = document.getElementById('mem-user').value.trim();
  const q = document.getElementById('mem-query').value.trim();
  const params = new URLSearchParams({ server_id: currentMemServerID, limit: 25, offset: 0 });
  if (userID) params.set('user_id', userID);
  if (q) params.set('q', q);

  fetch('/api/memories?' + params)
    .then(r => r.json())
    .then(data => renderMemories(data, currentMemServerID))
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
          '<button class="btn-edit">Edit</button>' +
          '<button class="btn-danger">Delete</button>' +
        '</div>' +
      '</div>';
    card.querySelector('.btn-edit').addEventListener('click', () => openEditModal(m.ID, m.Content, serverID));
    card.querySelector('.btn-danger').addEventListener('click', () => deleteMemory(m.ID, serverID));
    grid.appendChild(card);
  });
}

// --- Delete modal ---

let pendingDeleteID = null;
let pendingDeleteServerID = null;

function deleteMemory(id, serverID) {
  pendingDeleteID = id;
  pendingDeleteServerID = serverID;
  document.getElementById('delete-modal').showModal();
}

function closeDeleteModal() {
  document.getElementById('delete-modal').close();
  pendingDeleteID = null;
  pendingDeleteServerID = null;
}

function confirmDelete() {
  if (pendingDeleteID === null) return;
  const id = pendingDeleteID;
  const serverID = pendingDeleteServerID;
  closeDeleteModal();
  fetch('/api/memories/' + encodeURIComponent(id) + '?server_id=' + encodeURIComponent(serverID), { method: 'DELETE' })
    .then(r => { if (r.ok) searchMemories(); else r.text().then(t => alert('Delete failed: ' + t)); })
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
      if (r.ok) { closeEditModal(); searchMemories(); }
      else r.text().then(t => alert('Edit failed: ' + t));
    })
    .catch(() => alert('Edit failed.'));
}

// --- Monitor ---

function toggleMonitor() {
  const overlay = document.getElementById('monitor-overlay');
  overlay.hidden = !overlay.hidden;
}

// --- SSE ---

let sseConn = null;

function connectSSE() {
  if (sseConn) sseConn.close();
  sseConn = new EventSource('/api/events');

  sseConn.addEventListener('status', e => {
    try { renderMonitorAgents(JSON.parse(e.data)); } catch (_) {}
  });

  sseConn.addEventListener('config_reloaded', () => {
    loadAgents().then(() => router());
  });

  sseConn.onerror = () => setSseStatus(false);
  sseConn.onopen = () => setSseStatus(true);
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

function renderMonitorAgents(agentStatuses) {
  const grid = document.getElementById('agent-grid');
  const statusEl = document.getElementById('monitor-status');
  const dot = document.getElementById('agent-count-dot');
  grid.innerHTML = '';

  if (!agentStatuses || agentStatuses.length === 0) {
    statusEl.textContent = 'No active agents.';
    dot.className = 'status-dot';
    return;
  }

  statusEl.textContent = agentStatuses.length + ' active agent' + (agentStatuses.length !== 1 ? 's' : '');
  dot.className = 'status-dot active';

  agentStatuses.forEach(a => {
    const last = a.last_active ? new Date(a.last_active).toLocaleString() : '—';
    const qd = a.queue_depth || 0;
    let badgeClass = 'badge badge-red';
    if (qd === 0) badgeClass = 'badge badge-green';
    else if (qd <= 2) badgeClass = 'badge badge-amber';
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

function setStatus(elementId, msg, isError) {
  const el = document.getElementById(elementId);
  el.textContent = msg;
  el.className = 'status-msg' + (isError ? ' error' : '');
  if (!isError) setTimeout(() => { el.textContent = ''; }, 3000);
}

function esc(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// --- Boot ---

init();
