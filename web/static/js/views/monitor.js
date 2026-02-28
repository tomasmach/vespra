import { el, esc, emptyState, timeAgo } from '../components.js';

export function initMonitor(container) {
  container.innerHTML = '';

  // ── Header ──
  const header = el('div', { className: 'monitor-header' },
    el('span', { style: { fontFamily: 'var(--font-display)', fontSize: 'var(--text-lg)' } }, 'Live Monitor'),
    el('button', {
      className: 'btn btn-ghost btn-sm',
      type: 'button',
      onClick: () => {
        container.classList.remove('open');
      },
    }, 'Close'),
  );
  container.appendChild(header);

  // ── Content area ──
  const content = el('div', { className: 'monitor-content' });
  content.appendChild(emptyState('~', 'Waiting for status', 'No status events received yet.'));
  container.appendChild(content);

  // ── Listen for status events ──
  function handleStatus(e) {
    const status = e.detail;
    if (!status) return;

    const agents = status.agents || [];
    content.innerHTML = '';

    if (!agents.length) {
      content.appendChild(emptyState('~', 'No active agents', 'No agents are currently running.'));
      return;
    }

    // Group agents by server_id
    const grouped = {};
    for (const agent of agents) {
      const sid = agent.server_id || 'unknown';
      if (!grouped[sid]) grouped[sid] = [];
      grouped[sid].push(agent);
    }

    for (const [serverId, serverAgents] of Object.entries(grouped)) {
      for (const agent of serverAgents) {
        const card = el('div', { className: 'monitor-agent' });

        // Agent header
        const channels = agent.channels || [];
        const agentHeader = el('div', { className: 'monitor-agent-header' },
          el('span', {
            style: { fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' },
          }, esc(serverId)),
          el('span', { className: 'badge badge-lavender' }, String(channels.length) + ' channels'),
        );
        card.appendChild(agentHeader);

        // Channel list
        if (channels.length) {
          for (const ch of channels) {
            const depth = ch.queue_depth || 0;
            const maxDepth = 5;
            const fillPct = Math.min(100, (depth / maxDepth) * 100);
            const barColor = depth === 0
              ? 'var(--success)'
              : depth <= 2
                ? 'var(--warning)'
                : 'var(--danger)';

            const channelRow = el('div', { className: 'monitor-channel' },
              el('span', {
                style: { fontFamily: 'var(--font-mono)', minWidth: '120px' },
              }, esc(ch.channel_id || '')),
              el('span', {}, timeAgo(ch.last_active)),
              el('div', { className: 'queue-bar' },
                el('div', {
                  className: 'queue-bar-fill',
                  style: { width: fillPct + '%', background: barColor },
                }),
              ),
            );
            card.appendChild(channelRow);
          }
        }

        content.appendChild(card);
      }
    }
  }

  document.addEventListener('vespra:status', handleStatus);

  // Return cleanup function
  return function cleanup() {
    document.removeEventListener('vespra:status', handleStatus);
  };
}
