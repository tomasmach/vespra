import { API } from '../api.js';
import { el, toast, modePicker, providerPicker, section } from '../components.js';
import { navigate } from '../router.js';

export async function render(container, params) {
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);

  // Mutable form state
  const state = {
    response_mode: '',
    provider: '',
  };

  // ── Header ──
  wrap.appendChild(el('h2', { style: { marginBottom: 'var(--sp-6)' } }, 'New Agent'));

  // ── ID & Server ID ──
  const idInput = el('input', {
    className: 'input',
    type: 'text',
    placeholder: 'my-server',
  });
  const serverIdInput = el('input', {
    className: 'input',
    type: 'text',
    placeholder: '123456789',
  });

  const identitySection = section('IDENTITY',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'ID (required)'),
        idInput,
        el('span', { className: 'input-hint' }, 'Unique agent identifier.'),
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Server ID (required)'),
        serverIdInput,
        el('span', { className: 'input-hint' }, 'Discord guild ID.'),
      ),
    ),
  );
  wrap.appendChild(identitySection);

  // ── Connection ──
  const tokenInput = el('input', {
    className: 'input',
    type: 'password',
    placeholder: 'Leave blank to use default bot token',
  });
  const connectionSection = section('CONNECTION',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group full' },
        el('label', { className: 'input-label' }, 'Token (optional)'),
        tokenInput,
      ),
    ),
  );
  wrap.appendChild(connectionSection);

  // ── LLM Provider ──
  const providerPickerEl = providerPicker(state.provider, (val) => { state.provider = val; });
  const modelInput = el('input', {
    className: 'input',
    type: 'text',
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

  // ── Behavior ──
  const modePickerEl = modePicker(state.response_mode, '', (mode) => {
    state.response_mode = mode;
  });
  const languageInput = el('input', {
    className: 'input',
    type: 'text',
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

  // ── Storage ──
  const soulFileInput = el('input', {
    className: 'input',
    type: 'text',
    placeholder: '~/.config/vespra/soul.md',
  });
  const dbPathInput = el('input', {
    className: 'input',
    type: 'text',
    placeholder: 'auto',
  });

  const storageSection = section('FILES',
    el('div', { className: 'form-grid' },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Soul File (optional)'),
        soulFileInput,
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'DB Path (optional)'),
        dbPathInput,
      ),
    ),
  );
  wrap.appendChild(storageSection);

  // ── Create button ──
  const createBtn = el('button', {
    className: 'btn btn-primary btn-lg',
    type: 'button',
    onClick: async () => {
      const id = idInput.value.trim();
      const serverId = serverIdInput.value.trim();

      if (!id) {
        toast('Agent ID is required', 'error');
        idInput.focus();
        return;
      }
      if (!serverId) {
        toast('Server ID is required', 'error');
        serverIdInput.focus();
        return;
      }

      createBtn.disabled = true;
      try {
        const data = {
          id,
          server_id: serverId,
        };

        const tokenVal = tokenInput.value.trim();
        if (tokenVal) data.token = tokenVal;

        const soulVal = soulFileInput.value.trim();
        if (soulVal) data.soul_file = soulVal;

        const dbVal = dbPathInput.value.trim();
        if (dbVal) data.db_path = dbVal;

        if (state.response_mode) data.response_mode = state.response_mode;

        const langVal = languageInput.value.trim();
        if (langVal) data.language = langVal;

        if (state.provider) data.provider = state.provider;

        const modelVal = modelInput.value.trim();
        if (modelVal) data.model = modelVal;

        await API.createAgent(data);
        toast('Agent created', 'success');
        navigate('/agents/' + encodeURIComponent(id));
      } catch (err) {
        toast('Failed to create agent: ' + err.message, 'error');
      } finally {
        createBtn.disabled = false;
      }
    },
  }, 'Create Agent');

  const actions = el('div', {
    style: {
      marginTop: 'var(--sp-4)',
      paddingTop: 'var(--sp-4)',
      borderTop: '1px solid var(--night-border)',
    },
  }, createBtn);

  wrap.appendChild(actions);
}
