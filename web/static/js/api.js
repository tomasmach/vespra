// Centralized API module

const enc = encodeURIComponent;

function qs(params) {
  return Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([k, v]) => `${enc(k)}=${enc(v)}`)
    .join('&');
}

async function request(method, url, body, contentType) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    if (contentType === 'text') {
      opts.headers['Content-Type'] = 'text/plain';
      opts.body = body;
    } else {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
  }
  const res = await fetch(url, opts);
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(text || `HTTP ${res.status}`);
  }
  if (res.status === 204) return null;
  const ct = res.headers.get('Content-Type') || '';
  if (ct.includes('json')) return res.json();
  return res.text();
}

const get    = (url) => request('GET', url);
const post   = (url, body) => request('POST', url, body);
const put    = (url, body) => request('PUT', url, body);
const del    = (url) => request('DELETE', url);
const patch  = (url, body) => request('PATCH', url, body);

export const API = {
  // Agents
  listAgents:    ()              => get('/api/agents'),
  createAgent:   (data)          => post('/api/agents', data),
  updateAgent:   (id, data)      => put(`/api/agents/${enc(id)}`, data),
  deleteAgent:   (id)            => del(`/api/agents/${enc(id)}`),
  restartAgent:  (id)            => post(`/api/agents/${enc(id)}/restart`),

  // Souls (per-agent library)
  listSouls:     (id)            => get(`/api/agents/${enc(id)}/souls`),
  getSoul:       (id, name)      => get(`/api/agents/${enc(id)}/souls/${enc(name)}`),
  createSoul:    (id, data)      => post(`/api/agents/${enc(id)}/souls`, data),
  updateSoul:    (id, name, data)=> put(`/api/agents/${enc(id)}/souls/${enc(name)}`, data),
  deleteSoul:    (id, name)      => del(`/api/agents/${enc(id)}/souls/${enc(name)}`),
  activateSoul:  (id, name)      => post(`/api/agents/${enc(id)}/souls/${enc(name)}/activate`),
  getActiveSoul: (id)            => get(`/api/agents/${enc(id)}/soul`),
  setActiveSoul: (id, data)      => put(`/api/agents/${enc(id)}/soul`, data),

  // Global soul
  getGlobalSoul: ()              => get('/api/soul'),
  setGlobalSoul: (data)          => put('/api/soul', data),

  // Memories
  listMemories:  (params)        => get('/api/memories?' + qs(params)),
  deleteMemory:  (id, sid)       => del(`/api/memories/${enc(id)}?server_id=${enc(sid)}`),
  patchMemory:   (id, sid, data) => patch(`/api/memories/${enc(id)}?server_id=${enc(sid)}`, data),

  // Logs & Conversations
  getLogs:          (id, params)  => get(`/api/agents/${enc(id)}/logs?${qs(params)}`),
  getConversations: (id, params)  => get(`/api/agents/${enc(id)}/conversations?${qs(params)}`),

  // Config
  getConfig:     ()              => request('GET', '/api/config'),
  setConfig:     (toml)          => request('POST', '/api/config', toml, 'text'),

  // Status
  getStatus:     ()              => get('/api/status'),
};
