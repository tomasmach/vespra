import { API } from '../api.js';
import { el, esc, toast, modePicker, loading, emptyState, section } from '../components.js';

export async function render(container, params) {
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);
  wrap.appendChild(loading());

  let status;
  let soulData;
  let configData;
  let imageConfig;

  try {
    [status, soulData, configData, imageConfig] = await Promise.all([
      API.getStatus().catch(() => null),
      API.getGlobalSoul().catch(() => null),
      API.getConfig().catch(() => null),
      API.getImageConfig().catch(() => null),
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

  // ── 3. Image Generation ──
  const imgApiKeyInput = el('input', {
    className: 'input',
    type: 'password',
    placeholder: imageConfig && imageConfig.has_api_key ? '••••••••••••••••' : 'fal_...',
  });
  const imgModelInput = el('input', {
    className: 'input',
    type: 'text',
    value: (imageConfig && imageConfig.model) || '',
    placeholder: 'fal-ai/flux/schnell',
  });
  const imgImg2ImgModelInput = el('input', {
    className: 'input',
    type: 'text',
    value: (imageConfig && imageConfig.img2img_model) || '',
    placeholder: 'fal-ai/flux/dev/image-to-image',
  });

  // Three states: null (inherit/default), true (enabled), false (disabled)
  // Initialize from current config: null if not set, otherwise the boolean value
  const imgSafetyState = {
    value: (imageConfig && imageConfig.enable_safety_checker != null)
      ? imageConfig.enable_safety_checker
      : null
  };
  const imgSafetyBtn = el('button', {
    className: 'btn btn-sm btn-secondary',
    type: 'button',
    style: { minWidth: '100px' },
  });
  function updateSafetyBtn() {
    if (imgSafetyState.value === null) {
      imgSafetyBtn.textContent = 'Default';
      imgSafetyBtn.style.opacity = '0.6';
    } else if (imgSafetyState.value === true) {
      imgSafetyBtn.textContent = 'Enabled';
      imgSafetyBtn.style.opacity = '1';
    } else {
      imgSafetyBtn.textContent = 'Disabled';
      imgSafetyBtn.style.opacity = '0.5';
    }
  }
  imgSafetyBtn.addEventListener('click', () => {
    if (imgSafetyState.value === null) imgSafetyState.value = true;
    else if (imgSafetyState.value === true) imgSafetyState.value = false;
    else imgSafetyState.value = null;
    updateSafetyBtn();
  });
  updateSafetyBtn();

  const imgTimeoutInput = el('input', {
    className: 'input',
    type: 'number',
    value: (imageConfig && imageConfig.timeout_seconds) || 60,
    min: '10',
    max: '300',
    style: { width: '100px' },
  });

  const imgSaveBtn = el('button', {
    className: 'btn btn-sm',
    type: 'button',
    style: { marginTop: 'var(--sp-3)' },
    onClick: async () => {
      imgSaveBtn.disabled = true;
      imgSaveBtn.textContent = 'Saving...';
      try {
        const data = {};
        const keyVal = imgApiKeyInput.value.trim();
        if (keyVal) data.api_key = keyVal;
        const modelVal = imgModelInput.value.trim();
        if (modelVal) data.model = modelVal;
        data.img2img_model = imgImg2ImgModelInput.value.trim();
        // Always send enable_safety_checker: null means clear, true/false means set
        data.enable_safety_checker = imgSafetyState.value;
        const timeoutVal = parseInt(imgTimeoutInput.value, 10);
        if (timeoutVal > 0) data.timeout_seconds = timeoutVal;
        await API.setImageConfig(data);
        if (keyVal) imgApiKeyInput.value = '';
        toast('Image config saved', 'success');
      } catch (err) {
        toast('Failed to save image config: ' + err.message, 'error');
      } finally {
        imgSaveBtn.disabled = false;
        imgSaveBtn.textContent = 'Save';
      }
    },
  }, 'Save');

  const imgSection = section('IMAGE GENERATION',
    el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-4)' } },
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'API Key (fal.ai)'),
        imgApiKeyInput,
        el('span', { className: 'input-hint' },
          imageConfig && imageConfig.has_api_key
            ? 'A key is configured. Enter a new value to replace it.'
            : 'Enter your fal.ai API key to enable image generation.'),
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Model'),
        imgModelInput,
        el('span', { className: 'input-hint' }, 'Default: fal-ai/flux/schnell'),
      ),
      el('div', { className: 'input-group' },
        el('label', { className: 'input-label' }, 'Image-to-Image Model'),
        imgImg2ImgModelInput,
        el('span', { className: 'input-hint' }, 'Used when a reference image is attached. Default: fal-ai/flux/dev/image-to-image'),
      ),
      el('div', { style: { display: 'flex', gap: 'var(--sp-6)', alignItems: 'flex-start' } },
        el('div', { className: 'input-group' },
          el('label', { className: 'input-label' }, 'Safety Checker'),
          imgSafetyBtn,
        ),
        el('div', { className: 'input-group' },
          el('label', { className: 'input-label' }, 'Timeout (seconds)'),
          imgTimeoutInput,
        ),
      ),
      imgSaveBtn,
    ),
  );
  wrap.appendChild(imgSection);

  // ── 4. Raw Config (collapsible) ──
  const configDetails = el('details', { className: 'settings-section', style: { marginTop: 'var(--sp-4)' } });
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
