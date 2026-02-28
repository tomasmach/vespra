import { API } from '../api.js';
import { el, esc, toast, modePicker, loading, emptyState } from '../components.js';

export async function render(container, params) {
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);
  wrap.appendChild(loading());

  let status;
  let soulData;
  let configData;

  try {
    [status, soulData, configData] = await Promise.all([
      API.getStatus().catch(() => null),
      API.getGlobalSoul().catch(() => null),
      API.getConfig().catch(() => null),
    ]);
  } catch (err) {
    toast('Failed to load settings: ' + err.message, 'error');
    wrap.innerHTML = '';
    wrap.appendChild(emptyState('!', 'Failed to load', 'Could not fetch settings data.'));
    return;
  }

  wrap.innerHTML = '';

  // ── 1. Default Response Mode ──
  const modeSection = el('div', { className: 'settings-section' });
  modeSection.appendChild(el('span', { className: 'mono-label' }, 'DEFAULT RESPONSE MODE'));

  const currentMode = (status && status.default_mode) || '';
  const picker = modePicker(currentMode, '', () => {
    // Read-only display; changes require TOML edit
  });
  modeSection.appendChild(picker);

  modeSection.appendChild(
    el('div', {
      style: { marginTop: 'var(--sp-3)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' },
    }, 'This reflects the current global default mode. To change it, edit the raw config below.'),
  );

  wrap.appendChild(modeSection);

  // ── 2. Global Soul ──
  const soulSection = el('div', { className: 'settings-section' });
  soulSection.appendChild(el('span', { className: 'mono-label' }, 'GLOBAL SOUL'));

  const soulContent = (soulData && typeof soulData === 'object') ? (soulData.content || '') : (soulData || '');

  const soulTextarea = el('textarea', {
    className: 'code-editor',
    style: { width: '100%', minHeight: '200px' },
  });
  soulTextarea.value = soulContent;
  soulSection.appendChild(soulTextarea);

  if (soulData && soulData.path) {
    soulSection.appendChild(
      el('div', {
        style: { marginTop: 'var(--sp-2)', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' },
      }, 'Path: ' + esc(soulData.path)),
    );
  }

  const soulSaveBtn = el('button', {
    className: 'btn btn-sm',
    type: 'button',
    style: { marginTop: 'var(--sp-3)' },
    onClick: async () => {
      soulSaveBtn.disabled = true;
      soulSaveBtn.textContent = 'Saving...';
      try {
        await API.setGlobalSoul({ content: soulTextarea.value });
        toast('Global soul saved', 'success');
      } catch (err) {
        toast('Failed to save soul: ' + err.message, 'error');
      } finally {
        soulSaveBtn.disabled = false;
        soulSaveBtn.textContent = 'Save Soul';
      }
    },
  }, 'Save Soul');
  soulSection.appendChild(soulSaveBtn);

  wrap.appendChild(soulSection);

  // ── 3. Raw Config (collapsible) ──
  const configDetails = el('details', { className: 'settings-section' });
  const configSummary = el('summary', {
    className: 'mono-label',
    style: { cursor: 'pointer', userSelect: 'none' },
  }, 'RAW CONFIG');
  configDetails.appendChild(configSummary);

  configDetails.appendChild(
    el('div', {
      style: { marginBottom: 'var(--sp-3)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' },
    }, 'Advanced: edit the raw TOML configuration file.'),
  );

  const configTextarea = el('textarea', {
    className: 'code-editor',
    style: { width: '100%', minHeight: '300px' },
  });
  configTextarea.value = typeof configData === 'string' ? configData : (configData ? JSON.stringify(configData, null, 2) : '');
  configDetails.appendChild(configTextarea);

  const configSaveBtn = el('button', {
    className: 'btn btn-sm',
    type: 'button',
    style: { marginTop: 'var(--sp-3)' },
    onClick: async () => {
      configSaveBtn.disabled = true;
      configSaveBtn.textContent = 'Saving...';
      try {
        await API.setConfig(configTextarea.value);
        toast('Config saved and reloaded', 'success');
      } catch (err) {
        toast('Failed to save config: ' + err.message, 'error');
      } finally {
        configSaveBtn.disabled = false;
        configSaveBtn.textContent = 'Save Config';
      }
    },
  }, 'Save Config');
  configDetails.appendChild(configSaveBtn);

  wrap.appendChild(configDetails);
}
