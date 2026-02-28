import { API } from '../api.js';
import { el, esc, toast, modePicker, loading, emptyState } from '../components.js';

export async function render(container, params) {
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);
  wrap.appendChild(loading());

  let agents;
  try {
    agents = await API.listAgents();
  } catch (err) {
    toast('Failed to load agents: ' + err.message, 'error');
    wrap.innerHTML = '';
    wrap.appendChild(emptyState('!', 'Failed to load', 'Could not fetch agent data.'));
    return;
  }

  const agent = agents.find(a => a.id === params.id);
  if (!agent) {
    wrap.innerHTML = '';
    wrap.appendChild(emptyState('?', 'Agent not found', 'No agent with ID "' + esc(params.id) + '".'));
    return;
  }

  wrap.innerHTML = '';

  // ── Overview grid ──
  const grid = el('div', { className: 'overview-grid' });

  // 1. Response Mode card
  const modeCard = el('div', { className: 'card' });
  modeCard.appendChild(el('div', { className: 'mono-label overview-card-label' }, 'RESPONSE MODE'));

  const picker = modePicker(agent.response_mode, '', async (mode) => {
    try {
      await API.updateAgent(agent.id, { response_mode: mode });
      toast('Response mode updated', 'success');
    } catch (err) {
      toast('Failed to update mode: ' + err.message, 'error');
    }
  });
  modeCard.appendChild(picker);

  if (!agent.response_mode) {
    modeCard.appendChild(
      el('div', { className: 'overview-card-hint' }, 'Inheriting from global'),
    );
  }
  grid.appendChild(modeCard);

  // 2. Soul card
  const soulCard = el('div', { className: 'card' });
  soulCard.appendChild(el('div', { className: 'mono-label overview-card-label' }, 'ACTIVE SOUL'));
  soulCard.appendChild(
    el('div', { className: 'overview-card-value' },
      agent.soul_file || 'Global default',
    ),
  );
  soulCard.appendChild(
    el('div', { style: { marginTop: 'var(--sp-3)' } },
      el('a', { href: '/agents/' + encodeURIComponent(agent.id) + '/soul' }, 'Manage \u2192'),
    ),
  );
  grid.appendChild(soulCard);

  // 3. Provider/Model card
  const llmCard = el('div', { className: 'card' });
  llmCard.appendChild(el('div', { className: 'mono-label overview-card-label' }, 'LLM'));
  const llmValue = [agent.provider, agent.model].filter(Boolean).join(' / ');
  llmCard.appendChild(
    el('div', { className: 'overview-card-value' },
      llmValue || 'Inheriting global',
    ),
  );
  llmCard.appendChild(
    el('div', { style: { marginTop: 'var(--sp-3)' } },
      el('a', { href: '/agents/' + encodeURIComponent(agent.id) + '/config' }, 'Configure \u2192'),
    ),
  );
  grid.appendChild(llmCard);

  // 4. Info card
  const infoCard = el('div', { className: 'card' });
  infoCard.appendChild(el('div', { className: 'mono-label overview-card-label' }, 'INFO'));

  const channelCount = agent.channels ? Object.keys(agent.channels).length : 0;
  const ignoreCount = agent.ignore_users ? agent.ignore_users.length : 0;

  const tokenBadge = agent.has_token
    ? el('span', { className: 'badge badge-success' }, 'custom token')
    : el('span', { className: 'badge badge-muted' }, 'default token');

  const infoList = el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-2)' } },
    el('div', { style: { display: 'flex', alignItems: 'center', gap: 'var(--sp-2)' } },
      el('span', { className: 'mono-label' }, 'server_id'),
      el('span', { style: { fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', color: 'var(--cream)' } },
        esc(agent.server_id),
      ),
    ),
    el('div', { style: { display: 'flex', alignItems: 'center', gap: 'var(--sp-2)' } },
      el('span', { className: 'mono-label' }, 'token'),
      tokenBadge,
    ),
    el('div', { style: { fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' } },
      channelCount + ' channel override' + (channelCount !== 1 ? 's' : ''),
    ),
    el('div', { style: { fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' } },
      ignoreCount + ' ignored user' + (ignoreCount !== 1 ? 's' : ''),
    ),
    agent.db_path
      ? el('div', { style: { fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' } },
          esc(agent.db_path),
        )
      : null,
  );

  infoCard.appendChild(infoList);
  grid.appendChild(infoCard);

  wrap.appendChild(grid);
}
