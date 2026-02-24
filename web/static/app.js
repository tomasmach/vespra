'use strict';

// --- State ---

let agents = [];
let selectedAgentId = null;
let currentMemServerID = null;
let logsOffset = 0;
let convsOffset = 0;
const LOGS_LIMIT = 100;
const CONVS_LIMIT = 20;
let currentIgnoreUsers = [];
let currentChannels = [];

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

function renderConfigTab() {
  loadConfig();
}

function router() {
  const hash = location.hash;

  if (hash === '#/config') {
    selectedAgentId = null;
    renderAgentSidebar();
    showPanel('config');
    renderConfigTab();
    return;
  }

  if (hash === '#/agents/new') {
    selectedAgentId = null;
    renderAgentSidebar();
    clearNewAgentForm();
    showPanel('new-agent');
    return;
  }

  const m = hash.match(/^#\/agents\/([^/]+?)(?:\/(config|soul|memories|logs|conversations))?$/);
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
  logsOffset = 0;
  convsOffset = 0;
  document.getElementById('agent-detail-name').textContent = agent.id;
  document.getElementById('cfg-server-id').value = agent.server_id || '';
  document.getElementById('cfg-token').value = '';
  document.getElementById('cfg-soul-file').value = agent.soul_file || '';
  document.getElementById('cfg-db-path').value = agent.db_path || '';
  document.getElementById('cfg-response-mode').value = agent.response_mode || '';
  document.getElementById('cfg-language').value = agent.language || '';
  document.getElementById('cfg-provider').value = agent.provider || '';
  document.getElementById('cfg-model').value = agent.model || '';
  document.getElementById('cfg-status').textContent = '';
  currentIgnoreUsers = agent.ignore_users ? agent.ignore_users.slice() : [];
  currentChannels = agent.channels ? agent.channels.slice() : [];
  renderIgnoredUsers();
  renderChannels();
}

function renderDetailTab(tab) {
  ['config', 'soul', 'memories', 'logs', 'conversations'].forEach(t => {
    document.getElementById('dtab-' + t).classList.toggle('active', t === tab);
    document.getElementById('detail-' + t).hidden = (t !== tab);
  });

  if (tab === 'soul' && selectedAgentId) { loadSoul(selectedAgentId); }
  if (tab === 'memories' && selectedAgentId) {
    const agent = agents.find(a => a.id === selectedAgentId);
    if (agent) {
      currentMemServerID = agent.server_id;
      searchMemories();
    }
  }
  if (tab === 'logs' && selectedAgentId) loadLogs();
  if (tab === 'conversations' && selectedAgentId) loadConversations();
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
    language: document.getElementById('cfg-language').value.trim(),
    provider: document.getElementById('cfg-provider').value,
    model: document.getElementById('cfg-model').value.trim(),
    ignore_users: currentIgnoreUsers,
    channels: currentChannels,
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

// --- Ignored users ---

function renderIgnoredUsers() {
  const el = document.getElementById('ignored-users-list');
  if (!currentIgnoreUsers.length) {
    el.innerHTML = '<span class="text-gray-400 text-sm">No ignored users</span>';
    return;
  }
  el.innerHTML = currentIgnoreUsers.map((uid, i) =>
    '<div class="flex gap-2 items-center">' +
      '<span class="font-mono text-sm">' + esc(uid) + '</span>' +
      '<button class="btn-danger-sm" onclick="removeIgnoredUser(' + i + ')">Remove</button>' +
    '</div>'
  ).join('');
}

function addIgnoredUser() {
  const input = document.getElementById('new-ignore-user');
  const uid = input.value.trim();
  if (!uid) return;
  if (currentIgnoreUsers.includes(uid)) { input.value = ''; return; }
  currentIgnoreUsers.push(uid);
  input.value = '';
  renderIgnoredUsers();
}

function removeIgnoredUser(i) {
  currentIgnoreUsers.splice(i, 1);
  renderIgnoredUsers();
}

// --- Channels ---

function renderChannels() {
  const el = document.getElementById('channels-list');
  if (!currentChannels.length) {
    el.innerHTML = '<span class="text-gray-400 text-sm">No per-channel overrides</span>';
    return;
  }
  el.innerHTML = currentChannels.map((ch, i) =>
    '<div class="flex gap-2 items-center">' +
      '<span class="font-mono text-sm">' + esc(ch.id) + '</span>' +
      '<span class="text-xs text-zinc-500">' + esc(ch.response_mode) + '</span>' +
      '<button class="btn-danger-sm" onclick="removeChannel(' + i + ')">Remove</button>' +
    '</div>'
  ).join('');
}

function addChannel() {
  const idInput = document.getElementById('new-channel-id');
  const modeInput = document.getElementById('new-channel-mode');
  const id = idInput.value.trim();
  if (!id) return;
  if (currentChannels.some(ch => ch.id === id)) { idInput.value = ''; return; }
  currentChannels.push({ id: id, response_mode: modeInput.value });
  idInput.value = '';
  renderChannels();
}

function removeChannel(i) {
  currentChannels.splice(i, 1);
  renderChannels();
}

// --- Soul tab ---

let editingSoulName = null;

function loadSoul(agentId) {
  editingSoulName = null;
  document.getElementById('soul-editor').value = '';
  document.getElementById('soul-path-info').textContent = 'Loading…';
  document.getElementById('soul-status').textContent = '';
  document.getElementById('btn-activate-soul').hidden = true;
  document.getElementById('btn-delete-soul').hidden = true;
  hideNewSoulForm();

  fetch('/api/agents/' + encodeURIComponent(agentId) + '/souls')
    .then(r => r.json())
    .then(data => {
      renderAgentSouls(data);
      const activeSoul = (data.souls || []).find(s => s.active);
      if (activeSoul) {
        editAgentSoul(activeSoul.name);
      } else if ((data.souls || []).length > 0) {
        document.getElementById('soul-path-info').textContent = 'Select a soul from the library to edit.';
      } else {
        // No library souls — load legacy active soul
        fetch('/api/agents/' + encodeURIComponent(agentId) + '/soul')
          .then(r => r.json())
          .then(soul => {
            document.getElementById('soul-editor').value = soul.content || '';
            document.getElementById('soul-path-info').textContent = soul.using_default
              ? 'No soul file configured — will be auto-created on save.'
              : (soul.path || '');
          })
          .catch(() => {
            document.getElementById('soul-path-info').textContent = 'Failed to load soul.';
          });
      }
    })
    .catch(() => {
      document.getElementById('soul-path-info').textContent = 'Failed to load souls.';
    });
}

// --- Soul library ---

function loadAgentSouls(agentId) {
  fetch('/api/agents/' + encodeURIComponent(agentId) + '/souls')
    .then(r => r.json())
    .then(data => renderAgentSouls(data))
    .catch(() => setSoulLibStatus('Failed to load souls.', true));
}

function renderAgentSouls(data) {
  const select = document.getElementById('soul-select');
  const souls = data.souls || [];
  const prev = select.value;

  select.innerHTML = '<option value="">— select a soul —</option>';
  souls.forEach(s => {
    const opt = document.createElement('option');
    opt.value = s.name;
    opt.textContent = s.name + (s.active ? ' ★' : '');
    if (s.name === prev || s.name === editingSoulName) opt.selected = true;
    select.appendChild(opt);
  });

  // Update toolbar button visibility
  const hasSelection = !!editingSoulName;
  document.getElementById('btn-activate-soul').hidden = !hasSelection;
  document.getElementById('btn-delete-soul').hidden = !hasSelection;
}

function onSoulSelectChange() {
  const name = document.getElementById('soul-select').value;
  if (name) editAgentSoul(name);
}

function showNewSoulForm() {
  document.getElementById('soul-new-form').hidden = false;
  document.getElementById('new-soul-name').focus();
}

function hideNewSoulForm() {
  document.getElementById('soul-new-form').hidden = true;
  document.getElementById('new-soul-name').value = '';
}

function createAgentSoul() {
  if (!selectedAgentId) return;
  const name = document.getElementById('new-soul-name').value.trim();
  if (!name) { setSoulLibStatus('Name is required', true); return; }

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/souls', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, content: '' }),
  })
    .then(r => {
      if (r.ok) {
        hideNewSoulForm();
        editAgentSoul(name);
      } else {
        r.text().then(t => setSoulLibStatus(t || 'Create failed', true));
      }
    })
    .catch(() => setSoulLibStatus('Create failed', true));
}

function editAgentSoul(name) {
  if (!selectedAgentId) return;
  editingSoulName = name;
  document.getElementById('soul-editor').value = '';
  document.getElementById('soul-path-info').textContent = 'Loading…';
  document.getElementById('soul-select').value = name;
  document.getElementById('btn-activate-soul').hidden = false;
  document.getElementById('btn-delete-soul').hidden = false;

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/souls/' + encodeURIComponent(name))
    .then(r => {
      if (!r.ok) throw new Error('Soul not found');
      return r.json();
    })
    .then(data => {
      document.getElementById('soul-editor').value = data.content || '';
      document.getElementById('soul-path-info').textContent = data.path || '';
      loadAgentSouls(selectedAgentId);
    })
    .catch(() => {
      document.getElementById('soul-path-info').textContent = 'Failed to load soul.';
    });
}

function saveSoul() {
  if (!selectedAgentId) return;
  const content = document.getElementById('soul-editor').value;

  if (editingSoulName) {
    fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/souls/' + encodeURIComponent(editingSoulName), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
    })
      .then(r => {
        if (r.ok) setSoulStatus('Saved.', false);
        else r.text().then(t => setSoulStatus(t || 'Save failed', true));
      })
      .catch(() => setSoulStatus('Save failed', true));
  } else {
    // Legacy: save active soul directly
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
}

function activateEditingSoul() {
  if (!selectedAgentId || !editingSoulName) return;
  activateAgentSoul(editingSoulName);
}

function activateAgentSoul(name) {
  if (!selectedAgentId) return;

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/souls/' + encodeURIComponent(name) + '/activate', {
    method: 'POST',
  })
    .then(r => {
      if (r.ok) {
        setSoulStatus('Activated.', false);
        loadAgents().then(() => loadAgentSouls(selectedAgentId));
      } else {
        r.text().then(t => setSoulStatus(t || 'Activate failed', true));
      }
    })
    .catch(() => setSoulStatus('Activate failed', true));
}

function deleteSelectedSoul() {
  if (!selectedAgentId || !editingSoulName) return;
  deleteAgentSoul(editingSoulName);
}

function deleteAgentSoul(name) {
  if (!selectedAgentId) return;
  if (!confirm('Delete soul "' + name + '"?')) return;

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/souls/' + encodeURIComponent(name), {
    method: 'DELETE',
  })
    .then(r => {
      if (r.ok) {
        if (editingSoulName === name) {
          editingSoulName = null;
          document.getElementById('soul-editor').value = '';
          document.getElementById('soul-path-info').textContent = '';
        }
        loadAgentSouls(selectedAgentId);
      } else {
        r.text().then(t => setSoulLibStatus(t || 'Delete failed', true));
      }
    })
    .catch(() => setSoulLibStatus('Delete failed', true));
}

function setSoulLibStatus(msg, isError) {
  setStatus('soul-lib-status', msg, isError);
}

function setSoulStatus(msg, isError) {
  setStatus('soul-status', msg, isError);
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
  document.getElementById('na-language').value = '';
  document.getElementById('na-provider').value = '';
  document.getElementById('na-model').value = '';
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
    language: document.getElementById('na-language').value.trim(),
    provider: document.getElementById('na-provider').value,
    model: document.getElementById('na-model').value.trim(),
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

function restartSelectedAgent() {
  if (!selectedAgentId) return;
  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/restart', { method: 'POST' })
    .then(r => {
      if (!r.ok) return r.text().then(t => setCfgStatus(t || 'Restart failed', true));
      return r.json().then(data => {
        if (data && data.discord_session_restarted === false) {
          setCfgStatus('Agent state cleared. Discord session requires process restart.', false);
        } else {
          setCfgStatus('Agent restarted.', false);
        }
      });
    })
    .catch(() => setCfgStatus('Restart failed', true));
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

// --- Logs tab ---

function loadLogs() {
  if (!selectedAgentId) return;
  logsOffset = 0;
  fetchLogs();
}

function fetchLogs() {
  if (!selectedAgentId) return;
  const level = document.getElementById('log-level-filter').value;
  const params = new URLSearchParams({ limit: LOGS_LIMIT, offset: logsOffset });
  if (level) params.set('level', level);

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/logs?' + params)
    .then(r => r.json())
    .then(data => renderLogs(data))
    .catch(() => setStatus('logs-status', 'Failed to load logs.', true));
}

function renderLogs(data) {
  const rows = data.logs || [];
  const total = data.total || 0;
  const countEl = document.getElementById('logs-count');
  countEl.textContent = total + ' log' + (total !== 1 ? 's' : '');
  countEl.hidden = false;

  const table = document.getElementById('logs-table');
  if (rows.length === 0) {
    table.innerHTML = '<p class="empty-msg">No logs found.</p>';
    document.getElementById('logs-pagination').innerHTML = '';
    return;
  }

  table.innerHTML =
    '<table class="log-table">' +
    '<thead><tr><th>Time</th><th>Level</th><th>Message</th><th>Channel</th></tr></thead>' +
    '<tbody>' +
    rows.map(r => {
      const t = r.ts ? new Date(r.ts).toLocaleString() : '';
      const lvl = (r.level || '').toUpperCase();
      return '<tr>' +
        '<td class="log-time">' + esc(t) + '</td>' +
        '<td><span class="badge log-badge-' + esc(lvl.toLowerCase()) + '">' + esc(lvl) + '</span></td>' +
        '<td class="log-msg">' + esc(r.msg) + (r.attrs ? ' <span class="log-attrs">' + esc(fmtAttrs(r.attrs)) + '</span>' : '') + '</td>' +
        '<td class="log-channel">' + esc(r.channel_id || '') + '</td>' +
        '</tr>';
    }).join('') +
    '</tbody></table>';

  renderPagination('logs-pagination', total, logsOffset, LOGS_LIMIT, (newOffset) => {
    logsOffset = newOffset;
    fetchLogs();
  });
}

// --- Conversations tab ---

function loadConversations() {
  if (!selectedAgentId) return;
  convsOffset = 0;
  fetchConversations();
}

function fetchConversations() {
  if (!selectedAgentId) return;
  const channelID = document.getElementById('conv-channel').value.trim();
  const params = new URLSearchParams({ limit: CONVS_LIMIT, offset: convsOffset });
  if (channelID) params.set('channel_id', channelID);

  fetch('/api/agents/' + encodeURIComponent(selectedAgentId) + '/conversations?' + params)
    .then(r => r.json())
    .then(data => renderConversations(data))
    .catch(() => setStatus('conv-status', 'Failed to load conversations.', true));
}

function renderConversations(data) {
  const rows = data.conversations || [];
  const total = data.total || 0;
  const countEl = document.getElementById('conv-count');
  countEl.textContent = total + ' conversation' + (total !== 1 ? 's' : '');
  countEl.hidden = false;

  const list = document.getElementById('conv-list');
  if (rows.length === 0) {
    list.innerHTML = '<p class="empty-msg">No conversations found.</p>';
    document.getElementById('conv-pagination').innerHTML = '';
    return;
  }

  list.innerHTML = rows.map(r => {
    const t = r.ts ? new Date(r.ts).toLocaleString() : '';
    const tools = r.tool_calls ? '<div class="conv-tools"><span class="conv-tools-label">Tools:</span> ' + esc(r.tool_calls) + '</div>' : '';
    return '<div class="conv-card">' +
      '<div class="conv-header">' +
        '<span class="conv-channel">' + esc(r.channel_id || '') + '</span>' +
        '<span class="conv-time">' + esc(t) + '</span>' +
      '</div>' +
      '<div class="conv-user"><span class="conv-role">User</span> ' + esc(r.user_msg) + '</div>' +
      tools +
      '<div class="conv-response"><span class="conv-role">Bot</span> ' + esc(r.response) + '</div>' +
    '</div>';
  }).join('');

  renderPagination('conv-pagination', total, convsOffset, CONVS_LIMIT, (newOffset) => {
    convsOffset = newOffset;
    fetchConversations();
  });
}

// --- Pagination ---

function renderPagination(containerId, total, offset, limit, onNav) {
  const container = document.getElementById(containerId);
  const totalPages = Math.ceil(total / limit);
  const currentPage = Math.floor(offset / limit);
  if (totalPages <= 1) { container.innerHTML = ''; return; }

  let html = '<div class="pagination">';
  if (currentPage > 0) {
    html += '<button class="btn-secondary">← Prev</button>';
  }
  html += '<span class="page-info">Page ' + (currentPage + 1) + ' of ' + totalPages + '</span>';
  if (currentPage < totalPages - 1) {
    html += '<button class="btn-secondary">Next →</button>';
  }
  html += '</div>';
  container.innerHTML = html;

  const buttons = container.querySelectorAll('button');
  let btnIdx = 0;
  if (currentPage > 0) {
    buttons[btnIdx++].addEventListener('click', () => onNav((currentPage - 1) * limit));
  }
  if (currentPage < totalPages - 1) {
    buttons[btnIdx].addEventListener('click', () => onNav((currentPage + 1) * limit));
  }
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

function fmtAttrs(attrsJSON) {
  if (!attrsJSON) return '';
  try {
    const obj = JSON.parse(attrsJSON);
    return Object.entries(obj)
      .map(([k, v]) => k + '=' + (typeof v === 'object' ? JSON.stringify(v) : v))
      .join(' ');
  } catch { return attrsJSON; }
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
