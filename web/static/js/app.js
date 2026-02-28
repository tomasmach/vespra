import { initRouter, navigate } from './router.js';
import { API } from './api.js';
import { el, toast } from './components.js';
import { initMonitor } from './views/monitor.js';

// Global state
let agents = [];
let sseSource = null;
let monitorCleanup = null;

// ── Agent sidebar ──
async function loadAgents() {
  try {
    agents = await API.listAgents();
  } catch (e) {
    agents = [];
  }
  renderSidebar();
}

function renderSidebar() {
  const list = document.getElementById('agent-list');
  if (!list) return;
  list.innerHTML = '';
  for (const a of agents) {
    const item = el('a', {
      className: 'sidebar-item' + (location.pathname.includes(`/agents/${a.id}`) ? ' active' : ''),
      href: `/agents/${a.id}`,
    },
      el('span', { className: 'status-dot' }),
      el('span', {}, a.id),
    );
    list.appendChild(item);
  }
}

// ── SSE ──
function connectSSE() {
  const dot = document.getElementById('sse-status');
  sseSource = new EventSource('/api/events');

  sseSource.addEventListener('status', (e) => {
    try {
      const data = JSON.parse(e.data);
      document.dispatchEvent(new CustomEvent('vespra:status', { detail: data }));
      // Update sidebar dots
      updateSidebarStatus(data);
    } catch {}
  });

  sseSource.addEventListener('config_reloaded', () => {
    loadAgents();
    document.dispatchEvent(new CustomEvent('vespra:config-reloaded'));
    toast('Config reloaded', 'info');
  });

  sseSource.onopen = () => { if (dot) dot.className = 'status-dot-sm online'; };
  sseSource.onerror = () => { if (dot) dot.className = 'status-dot-sm error'; };
}

function updateSidebarStatus(statuses) {
  const items = document.querySelectorAll('#agent-list .sidebar-item');
  const activeServers = new Set(statuses.map(s => s.server_id));
  items.forEach(item => {
    const dot = item.querySelector('.status-dot');
    if (!dot) return;
    const agent = agents.find(a => `/agents/${a.id}` === item.getAttribute('href'));
    if (agent && activeServers.has(agent.server_id)) {
      dot.classList.add('online');
    } else {
      dot.classList.remove('online');
    }
  });
}

// ── Tab navigation for agent views ──
const agentTabs = [
  { id: 'overview', label: 'Overview', path: '' },
  { id: 'config', label: 'Config', path: '/config' },
  { id: 'soul', label: 'Soul', path: '/soul' },
  { id: 'channels', label: 'Channels', path: '/channels' },
  { id: 'memories', label: 'Memories', path: '/memories' },
  { id: 'logs', label: 'Logs', path: '/logs' },
  { id: 'conversations', label: 'Conversations', path: '/conversations' },
];

function renderTabs(agentId, activeTab) {
  const container = document.getElementById('tab-nav-container');
  if (!container) return;
  container.innerHTML = '';
  const nav = el('div', { className: 'tab-nav' });
  for (const tab of agentTabs) {
    const isActive = (tab.id === 'overview' && !activeTab) || tab.id === activeTab;
    const a = el('a', {
      className: 'tab-item' + (isActive ? ' active' : ''),
      href: `/agents/${agentId}${tab.path}`,
    }, tab.label);
    nav.appendChild(a);
  }
  container.appendChild(nav);
}

function clearTabs() {
  const container = document.getElementById('tab-nav-container');
  if (container) container.innerHTML = '';
}

// ── Breadcrumb ──
function setBreadcrumb(parts) {
  const bc = document.getElementById('breadcrumb');
  if (!bc) return;
  bc.innerHTML = '';
  for (let i = 0; i < parts.length; i++) {
    if (i > 0) bc.appendChild(document.createTextNode(' / '));
    if (parts[i].href) {
      bc.appendChild(el('a', { href: parts[i].href }, parts[i].label));
    } else {
      bc.appendChild(el('span', {}, parts[i].label));
    }
  }
}

// ── View routing ──
async function handleView(view, params) {
  const content = document.getElementById('content');
  if (!content) return;
  content.innerHTML = '';

  // Update sidebar active state
  renderSidebar();

  switch (view) {
    case 'dashboard': {
      clearTabs();
      setBreadcrumb([{ label: 'Dashboard' }]);
      const mod = await import('./views/dashboard.js');
      await mod.render(content, params);
      break;
    }
    case 'new-agent': {
      clearTabs();
      setBreadcrumb([{ label: 'Dashboard', href: '/' }, { label: 'New Agent' }]);
      const mod = await import('./views/new-agent.js');
      await mod.render(content, params);
      break;
    }
    case 'agent-overview': {
      renderTabs(params.id, 'overview');
      setBreadcrumb([{ label: 'Dashboard', href: '/' }, { label: params.id }]);
      const mod = await import('./views/agent-overview.js');
      await mod.render(content, params);
      break;
    }
    case 'agent-tab': {
      renderTabs(params.id, params.tab);
      setBreadcrumb([{ label: 'Dashboard', href: '/' }, { label: params.id, href: `/agents/${params.id}` }, { label: params.tab }]);
      const mod = await import(`./views/agent-${params.tab}.js`);
      await mod.render(content, params);
      break;
    }
    case 'settings': {
      clearTabs();
      setBreadcrumb([{ label: 'Dashboard', href: '/' }, { label: 'Settings' }]);
      const mod = await import('./views/settings.js');
      await mod.render(content, params);
      break;
    }
    default: {
      clearTabs();
      setBreadcrumb([{ label: 'Dashboard' }]);
      const mod = await import('./views/dashboard.js');
      await mod.render(content, params);
    }
  }
}

// ── Monitor ──
function setupMonitor() {
  const btn = document.getElementById('monitor-btn');
  const overlay = document.getElementById('monitor-overlay');
  const monitorContent = document.getElementById('monitor-content');

  if (btn && overlay) {
    btn.addEventListener('click', () => {
      overlay.classList.toggle('open');
      if (overlay.classList.contains('open') && !monitorCleanup) {
        monitorCleanup = initMonitor(monitorContent);
      }
    });
  }
}

// ── Keyboard shortcuts ──
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    const overlay = document.getElementById('monitor-overlay');
    if (overlay && overlay.classList.contains('open')) {
      overlay.classList.remove('open');
    }
  }
});

// ── Init ──
async function init() {
  await loadAgents();
  connectSSE();
  setupMonitor();
  initRouter(handleView);
}

init();
