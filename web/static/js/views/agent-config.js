import { API } from '../api.js';
import { el, esc, toast, modePicker, providerPicker, confirmDialog, section, loading, emptyState } from '../components.js';
import { navigate } from '../router.js';

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

  // Mutable state for the form
  const state = {
    response_mode: agent.response_mode || '',
    provider: agent.provider || '',
    ignore_users: agent.ignore_users ? [...agent.ignore_users] : [],
  };

  // ── IDENTITY section ──
  const identitySection = section('IDENTITY',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Agent ID'),
        el('input', { className: 'input', type: 'text', value: agent.id, readonly: '', disabled: '' }),
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Server ID'),
        el('input', { className: 'input', type: 'text', value: agent.server_id, readonly: '', disabled: '' }),
      ),
    ),
  );
  wrap.appendChild(identitySection);

  // ── CONNECTION section ──
  const tokenInput = el('input', {
    className: 'input',
    type: 'password',
    placeholder: 'Leave blank to keep current',
  });
  const connectionSection = section('CONNECTION',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group full' },
        el('label', { className: 'input-label' }, 'Token'),
        tokenInput,
        el('span', { className: 'input-hint' }, 'Discord bot token. Leave blank to keep the existing token.'),
      ),
    ),
  );
  wrap.appendChild(connectionSection);

  // ── LLM PROVIDER section ──
  const providerPickerEl = providerPicker(state.provider, (val) => { state.provider = val; });
  const modelInput = el('input', {
    className: 'input',
    type: 'text',
    value: agent.model || '',
    placeholder: 'e.g. gpt-4o, glm-4.7, kimi-k2.5',
  });

  const llmSection = section('LLM PROVIDER',
    el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-4)' } },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Provider'),
        providerPickerEl,
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Model'),
        modelInput,
      ),
    ),
  );
  wrap.appendChild(llmSection);

  // ── BEHAVIOR section ──
  const modePickerEl = modePicker(state.response_mode, '', (mode) => {
    state.response_mode = mode;
  });

  const languageInput = el('input', {
    className: 'input',
    type: 'text',
    value: agent.language || '',
    placeholder: 'e.g. Czech, Spanish',
  });

  const behaviorSection = section('BEHAVIOR',
    el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-4)' } },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Response Mode'),
        modePickerEl,
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Language'),
        languageInput,
      ),
    ),
  );
  wrap.appendChild(behaviorSection);

  // ── STORAGE section ──
  const dbPathInput = el('input', {
    className: 'input',
    type: 'text',
    value: agent.db_path || '',
    placeholder: 'auto',
  });

  const storageSection = section('STORAGE',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group full' },
        el('label', { className: 'input-label' }, 'DB Path'),
        dbPathInput,
        el('span', { className: 'input-hint' }, 'SQLite database file path. Leave blank for auto.'),
      ),
    ),
  );
  wrap.appendChild(storageSection);

  // ── IGNORED USERS section ──
  const ignoreListContainer = el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-2)' } });
  const ignoreAddInput = el('input', {
    className: 'input',
    type: 'text',
    placeholder: 'Discord User ID',
    style: { flex: '1' },
  });

  function renderIgnoreList() {
    ignoreListContainer.innerHTML = '';
    for (const uid of state.ignore_users) {
      const row = el('div', {
        style: {
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--sp-2)',
          padding: 'var(--sp-2) var(--sp-3)',
          background: 'var(--night)',
          borderRadius: 'var(--radius-sm)',
          border: '1px solid var(--night-border)',
        },
      },
        el('span', { style: { flex: '1', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' } }, esc(uid)),
        el('button', {
          className: 'btn btn-danger btn-sm',
          type: 'button',
          onClick: () => {
            state.ignore_users = state.ignore_users.filter(u => u !== uid);
            renderIgnoreList();
          },
        }, 'Remove'),
      );
      ignoreListContainer.appendChild(row);
    }
    if (!state.ignore_users.length) {
      ignoreListContainer.appendChild(
        el('div', { style: { fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' } }, 'No ignored users.'),
      );
    }
  }
  renderIgnoreList();

  const addIgnoreBtn = el('button', {
    className: 'btn btn-secondary btn-sm',
    type: 'button',
    onClick: () => {
      const val = ignoreAddInput.value.trim();
      if (!val) return;
      if (state.ignore_users.includes(val)) {
        toast('User already in ignore list', 'error');
        return;
      }
      state.ignore_users.push(val);
      ignoreAddInput.value = '';
      renderIgnoreList();
    },
  }, 'Add');

  const ignoreSection = section('IGNORED USERS',
    ignoreListContainer,
    el('div', { style: { display: 'flex', gap: 'var(--sp-2)', marginTop: 'var(--sp-3)' } },
      ignoreAddInput,
      addIgnoreBtn,
    ),
  );
  wrap.appendChild(ignoreSection);

  // ── Action buttons ──
  const saveBtn = el('button', {
    className: 'btn btn-primary',
    type: 'button',
    onClick: async () => {
      saveBtn.disabled = true;
      try {
        const data = {
          server_id: agent.server_id,
          soul_file: agent.soul_file || '',
          response_mode: state.response_mode,
          language: languageInput.value.trim(),
          provider: state.provider,
          model: modelInput.value.trim(),
          db_path: dbPathInput.value.trim(),
          ignore_users: state.ignore_users,
        };
        const tokenVal = tokenInput.value.trim();
        if (tokenVal) {
          data.token = tokenVal;
        }
        await API.updateAgent(agent.id, data);
        toast('Agent config saved', 'success');
      } catch (err) {
        toast('Failed to save: ' + err.message, 'error');
      } finally {
        saveBtn.disabled = false;
      }
    },
  }, 'Save');

  const deleteBtn = el('button', {
    className: 'btn btn-danger',
    type: 'button',
    onClick: async () => {
      const confirmed = await confirmDialog(
        'Delete Agent',
        'Are you sure you want to delete agent "' + agent.id + '"? This cannot be undone.',
      );
      if (!confirmed) return;
      try {
        await API.deleteAgent(agent.id);
        toast('Agent deleted', 'success');
        navigate('/');
      } catch (err) {
        toast('Failed to delete: ' + err.message, 'error');
      }
    },
  }, 'Delete');

  const actions = el('div', {
    style: {
      display: 'flex',
      gap: 'var(--sp-3)',
      marginTop: 'var(--sp-4)',
      paddingTop: 'var(--sp-4)',
      borderTop: '1px solid var(--night-border)',
    },
  }, saveBtn, deleteBtn);

  wrap.appendChild(actions);
}
