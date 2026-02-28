import { API } from '../api.js';
import { el, esc, toast, emptyState, loading } from '../components.js';
import { navigate } from '../router.js';

export async function render(container, params) {
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);

  // Show loading state
  wrap.appendChild(loading());

  let agents;
  let status;
  try {
    [agents, status] = await Promise.all([
      API.listAgents(),
      API.getStatus().catch(() => null),
    ]);
  } catch (err) {
    toast('Failed to load dashboard: ' + err.message, 'error');
    wrap.innerHTML = '';
    wrap.appendChild(emptyState('!', 'Failed to load', 'Could not fetch agent data.'));
    return;
  }

  wrap.innerHTML = '';

  // ── Hero ──
  const sseConnected = status && status.sse !== undefined ? status.sse : null;
  const sseBadge = sseConnected === true
    ? el('span', { className: 'badge badge-success' }, 'connected')
    : sseConnected === false
      ? el('span', { className: 'badge badge-danger' }, 'disconnected')
      : el('span', { className: 'badge badge-muted' }, 'unknown');

  const hero = el('div', { className: 'dashboard-hero' },
    el('h1', {}, 'Vespra'),
    el('p', { className: 'mono-label' }, 'Dashboard'),
    el('div', { className: 'dashboard-stats' },
      el('div', { className: 'stat-item' },
        el('span', { className: 'stat-value' }, String(agents.length)),
        'agents',
      ),
      el('div', { className: 'stat-item' },
        'SSE ', sseBadge,
      ),
    ),
  );
  wrap.appendChild(hero);

  // ── Agent cards ──
  if (!agents.length) {
    const empty = emptyState(
      '+',
      'No agents configured',
      el('a', { href: '/agents/new' }, 'Create your first agent'),
    );
    wrap.appendChild(empty);
  } else {
    const grid = el('div', { className: 'card-grid' });

    for (const agent of agents) {
      const soulDisplay = agent.soul_file || 'default';
      const modeDisplay = agent.response_mode || 'inherit';
      const providerModel = [agent.provider, agent.model]
        .filter(Boolean)
        .join(' / ') || 'global default';

      const card = el('div', {
        className: 'card card-clickable',
        onClick: () => navigate('/agents/' + encodeURIComponent(agent.id)),
      },
        el('h3', { style: { fontFamily: 'var(--font-display)', marginBottom: 'var(--sp-2)' } },
          esc(agent.id),
        ),
        el('div', { className: 'mono-label', style: { marginBottom: 'var(--sp-3)' } },
          esc(agent.server_id),
        ),
        el('div', { style: { display: 'flex', alignItems: 'center', gap: 'var(--sp-2)', flexWrap: 'wrap' } },
          el('span', { style: { fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' } }, soulDisplay),
          el('span', { className: 'badge badge-amber' }, modeDisplay),
        ),
        el('div', {
          style: {
            marginTop: 'var(--sp-2)',
            fontFamily: 'var(--font-mono)',
            fontSize: 'var(--text-xs)',
            color: 'var(--cream-muted)',
          },
        }, providerModel),
      );

      grid.appendChild(card);
    }

    wrap.appendChild(grid);
  }

  // ── Footer link ──
  const footer = el('div', { style: { marginTop: 'var(--sp-8)' } },
    el('a', { href: '/settings' }, 'Settings'),
  );
  wrap.appendChild(footer);
}
