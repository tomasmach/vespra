// SPA router using History API

const routes = [
  { pattern: /^\/$/, view: 'dashboard' },
  { pattern: /^\/agents\/new$/, view: 'new-agent' },
  { pattern: /^\/agents\/([^/]+)\/(config|soul|channels|memories|logs|conversations)$/, view: 'agent-tab', params: ['id', 'tab'] },
  { pattern: /^\/agents\/([^/]+)$/, view: 'agent-overview', params: ['id'] },
  { pattern: /^\/settings$/, view: 'settings' },
];

let currentView = null;
let viewHandler = null;

export function initRouter(handler) {
  viewHandler = handler;
  window.addEventListener('popstate', () => resolve());
  // Intercept clicks on internal links
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[href]');
    if (!a || a.target || a.origin !== location.origin) return;
    if (a.pathname.startsWith('/api/')) return;
    e.preventDefault();
    navigate(a.pathname);
  });
  resolve();
}

export function navigate(path) {
  if (path === location.pathname) {
    resolve();
    return;
  }
  history.pushState(null, '', path);
  resolve();
}

function resolve() {
  const path = location.pathname;
  for (const route of routes) {
    const match = path.match(route.pattern);
    if (match) {
      const params = {};
      (route.params || []).forEach((name, i) => params[name] = decodeURIComponent(match[i + 1]));
      const key = route.view + ':' + JSON.stringify(params);
      if (key !== currentView) {
        currentView = key;
        viewHandler(route.view, params);
      }
      return;
    }
  }
  // Fallback to dashboard
  viewHandler('dashboard', {});
}
