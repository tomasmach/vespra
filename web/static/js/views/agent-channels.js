import { API } from '../api.js';
import { el, toast, modePicker, loading, emptyState } from '../components.js';

export async function render(container, params) {
  const agentId = params.id;
  container.innerHTML = '';
  container.appendChild(loading());

  let agent = null;

  try {
    const agents = await API.listAgents();
    agent = (agents || []).find(a => a.id === agentId || a.server_id === agentId);
    if (!agent) throw new Error('Agent not found');
  } catch (err) {
    container.innerHTML = '';
    toast('Failed to load agent: ' + err.message, 'error');
    container.appendChild(emptyState('!', 'Failed to load agent', err.message));
    return;
  }

  function renderView() {
    container.innerHTML = '';
    const wrap = el('div', { className: 'fade-in' });

    // Info card
    const agentMode = agent.response_mode || 'smart';
    const modeInherited = !agent.response_mode ? 'inherited' : 'set';
    const infoCard = el('div', { className: 'card', style: { marginBottom: 'var(--sp-6)' } },
      el('div', { style: { fontSize: 'var(--text-sm)', color: 'var(--cream-muted)', lineHeight: '1.6' } },
        'Response mode priority: ',
        el('strong', { style: { color: 'var(--cream)' } }, 'Channel'),
        ' \u2192 ',
        el('strong', { style: { color: 'var(--cream)' } }, 'Agent'),
        ' \u2192 ',
        el('strong', { style: { color: 'var(--cream)' } }, 'Global'),
        '. Current agent mode: ',
        el('span', { className: 'badge badge-amber' }, agentMode),
        ' (' + modeInherited + ').',
      ),
    );
    wrap.appendChild(infoCard);

    // Channel list
    const channels = agent.channels || [];
    const listSection = el('div', { style: { marginBottom: 'var(--sp-6)' } });

    if (channels.length === 0) {
      listSection.appendChild(
        el('div', { style: { color: 'var(--cream-muted)', fontSize: 'var(--text-sm)', padding: 'var(--sp-4) 0' } },
          'No per-channel overrides configured. All channels use the agent mode.',
        ),
      );
    }

    for (let i = 0; i < channels.length; i++) {
      const ch = channels[i];
      const row = el('div', { className: 'channel-row' });

      row.appendChild(el('span', { className: 'channel-id' }, ch.id));

      const picker = modePicker(ch.response_mode, agentMode, (mode) => {
        channels[i] = { ...channels[i], response_mode: mode };
        saveChannels(channels);
      });
      row.appendChild(picker);

      const removeBtn = el('button', {
        className: 'btn btn-ghost btn-sm btn-danger',
        style: { marginLeft: 'auto' },
        onClick: () => {
          channels.splice(i, 1);
          agent.channels = channels;
          saveChannels(channels);
          renderView();
        },
      }, 'Remove');
      row.appendChild(removeBtn);

      listSection.appendChild(row);
    }

    wrap.appendChild(listSection);

    // Add channel form
    const formCard = el('div', { className: 'card' });
    const formTitle = el('div', { className: 'mono-label', style: { marginBottom: 'var(--sp-3)' } }, 'Add Channel Override');

    const formRow = el('div', { style: { display: 'flex', alignItems: 'flex-end', gap: 'var(--sp-3)', flexWrap: 'wrap' } });

    const channelIdInput = el('input', { className: 'input', placeholder: 'e.g. 123456789', type: 'text' });
    const idGroup = el('div', { className: 'input-group' },
      el('label', { className: 'input-label' }, 'Channel ID'),
      channelIdInput,
    );

    let newMode = 'smart';
    const modeGroup = el('div', { className: 'input-group' },
      el('label', { className: 'input-label' }, 'Response Mode'),
      modePicker(newMode, null, (m) => { newMode = m; }),
    );

    const addBtn = el('button', { className: 'btn btn-primary', onClick: () => handleAdd() }, 'Add');

    formRow.append(idGroup, modeGroup, addBtn);
    formCard.append(formTitle, formRow);
    wrap.appendChild(formCard);

    container.appendChild(wrap);

    async function handleAdd() {
      const channelId = channelIdInput.value.trim();
      if (!channelId) {
        toast('Channel ID is required', 'error');
        return;
      }

      const exists = channels.some(c => c.id === channelId);
      if (exists) {
        toast('Channel already configured', 'error');
        return;
      }

      channels.push({ id: channelId, response_mode: newMode });
      agent.channels = channels;
      await saveChannels(channels);
      renderView();
    }
  }

  async function saveChannels(channels) {
    try {
      const all = await API.listAgents();
      const current = all.find(a => a.id === agentId || a.server_id === agentId) || agent;
      await API.updateAgent(agentId, { ...current, channels });
      agent.channels = channels;
      toast('Channels updated', 'success');
    } catch (err) {
      toast('Failed to save channels: ' + err.message, 'error');
    }
  }

  renderView();
}
