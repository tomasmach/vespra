import { API } from '../api.js';
import { el, esc, toast, confirmDialog, loading, emptyState } from '../components.js';

export async function render(container, params) {
  const agentId = params.id;
  container.innerHTML = '';
  container.appendChild(loading());

  let souls = [];
  let soulFile = '';
  let selected = null; // { type: 'global' } or { type: 'soul', name }
  let editorContent = '';
  let editorPath = '';
  let editorIsActive = false;

  try {
    const data = await API.listSouls(agentId);
    souls = data.souls || [];
    soulFile = data.soul_file || '';
  } catch (err) {
    container.innerHTML = '';
    toast('Failed to load souls: ' + err.message, 'error');
    container.appendChild(emptyState('!', 'Failed to load souls', err.message));
    return;
  }

  function renderView() {
    container.innerHTML = '';
    const layout = el('div', { className: 'soul-layout fade-in' });

    // Left panel: soul list
    const list = el('div', { className: 'soul-list' });

    // Global default item
    const globalItem = el('div', {
      className: 'soul-item global' + (selected && selected.type === 'global' ? ' active' : ''),
      onClick: () => selectGlobal(),
    }, 'Global Default');
    list.appendChild(globalItem);

    // Separator
    list.appendChild(el('hr', { style: { border: 'none', borderTop: '1px solid var(--night-border)', margin: 'var(--sp-2) 0' } }));

    // Per-agent souls
    for (const soul of souls) {
      const isActive = soul.active;
      const isSelected = selected && selected.type === 'soul' && selected.name === soul.name;
      let cls = 'soul-item';
      if (isActive) cls += ' active';
      if (isSelected) cls += ' active';

      const item = el('div', {
        className: cls,
        onClick: () => selectSoul(soul.name),
      },
        el('span', {}, soul.name),
        isActive ? el('span', { className: 'badge badge-amber', style: { marginLeft: 'auto' } }, 'active') : null,
      );
      list.appendChild(item);
    }

    // New soul button
    const newBtn = el('button', {
      className: 'btn btn-ghost btn-sm',
      style: { marginTop: 'auto', border: '1px dashed var(--night-border)', width: '100%' },
      onClick: () => showNewSoulForm(list, newBtn),
    }, '+ New Soul');
    list.appendChild(newBtn);

    // Right panel: editor
    const editor = el('div', { className: 'soul-editor-panel' });

    if (!selected) {
      editor.appendChild(emptyState('~', 'Select a soul to edit'));
    } else {
      // Header
      const headerLeft = el('div', { style: { display: 'flex', alignItems: 'center', gap: 'var(--sp-3)' } });
      const title = selected.type === 'global' ? 'Global Default' : selected.name;
      headerLeft.appendChild(el('h3', {}, title));
      if (editorIsActive && selected.type === 'soul') {
        headerLeft.appendChild(el('span', { className: 'badge badge-amber' }, 'active'));
      }

      const header = el('div', { className: 'soul-editor-header' });
      header.appendChild(headerLeft);
      if (editorPath) {
        header.appendChild(el('span', { className: 'mono-label' }, editorPath));
      }
      editor.appendChild(header);

      // Action bar
      const actions = el('div', { style: { display: 'flex', gap: 'var(--sp-2)' } });

      const saveBtn = el('button', { className: 'btn btn-primary', onClick: () => handleSave(textarea) }, 'Save');
      actions.appendChild(saveBtn);

      if (selected.type === 'soul' && !editorIsActive) {
        const activateBtn = el('button', { className: 'btn btn-secondary', onClick: () => handleActivate() }, 'Activate');
        actions.appendChild(activateBtn);
      }

      if (selected.type === 'soul' && !editorIsActive) {
        const deleteBtn = el('button', { className: 'btn btn-danger', onClick: () => handleDelete() }, 'Delete');
        actions.appendChild(deleteBtn);
      }

      editor.appendChild(actions);

      // Textarea
      const textarea = el('textarea', {
        className: 'code-editor soul-editor-textarea',
      });
      textarea.value = editorContent;
      editor.appendChild(textarea);
    }

    layout.appendChild(list);
    layout.appendChild(editor);
    container.appendChild(layout);
  }

  async function selectGlobal() {
    try {
      const data = await API.getGlobalSoul();
      selected = { type: 'global' };
      editorContent = data.content || '';
      editorPath = data.path || '';
      editorIsActive = false;
      renderView();
    } catch (err) {
      toast('Failed to load global soul: ' + err.message, 'error');
    }
  }

  async function selectSoul(name) {
    try {
      const data = await API.getSoul(agentId, name);
      const activeSoul = souls.find(s => s.active);
      selected = { type: 'soul', name };
      editorContent = data.content || '';
      editorPath = data.path || '';
      editorIsActive = activeSoul && activeSoul.name === name;
      renderView();
    } catch (err) {
      toast('Failed to load soul: ' + err.message, 'error');
    }
  }

  async function handleSave(textarea) {
    const content = textarea.value;
    try {
      if (selected.type === 'global') {
        await API.setGlobalSoul({ content });
      } else {
        await API.updateSoul(agentId, selected.name, { content });
      }
      editorContent = content;
      toast('Soul saved', 'success');
    } catch (err) {
      toast('Failed to save: ' + err.message, 'error');
    }
  }

  async function handleActivate() {
    if (!selected || selected.type !== 'soul') return;
    try {
      await API.activateSoul(agentId, selected.name);
      toast('Soul activated: ' + selected.name, 'success');
      await reloadList();
      await selectSoul(selected.name);
    } catch (err) {
      toast('Failed to activate: ' + err.message, 'error');
    }
  }

  async function handleDelete() {
    if (!selected || selected.type !== 'soul') return;
    const ok = await confirmDialog('Delete Soul', `Delete "${selected.name}"? This cannot be undone.`);
    if (!ok) return;
    try {
      await API.deleteSoul(agentId, selected.name);
      toast('Soul deleted', 'success');
      selected = null;
      editorContent = '';
      editorPath = '';
      editorIsActive = false;
      await reloadList();
      renderView();
    } catch (err) {
      toast('Failed to delete: ' + err.message, 'error');
    }
  }

  async function reloadList() {
    try {
      const data = await API.listSouls(agentId);
      souls = data.souls || [];
      soulFile = data.soul_file || '';
    } catch (err) {
      toast('Failed to reload souls: ' + err.message, 'error');
    }
  }

  function showNewSoulForm(list, newBtn) {
    newBtn.remove();

    const form = el('div', { style: { display: 'flex', flexDirection: 'column', gap: 'var(--sp-2)', marginTop: 'auto' } });
    const nameInput = el('input', { className: 'input', placeholder: 'Soul name', type: 'text' });
    const createBtn = el('button', { className: 'btn btn-primary btn-sm', onClick: () => handleCreate(nameInput.value) }, 'Create');
    const cancelBtn = el('button', {
      className: 'btn btn-ghost btn-sm',
      onClick: () => renderView(),
    }, 'Cancel');
    const btnRow = el('div', { style: { display: 'flex', gap: 'var(--sp-2)' } }, createBtn, cancelBtn);

    form.appendChild(nameInput);
    form.appendChild(btnRow);
    list.appendChild(form);

    nameInput.focus();

    nameInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') handleCreate(nameInput.value);
      if (e.key === 'Escape') renderView();
    });
  }

  async function handleCreate(name) {
    name = name.trim();
    if (!name) {
      toast('Soul name is required', 'error');
      return;
    }
    try {
      await API.createSoul(agentId, { name, content: '' });
      toast('Soul created: ' + name, 'success');
      await reloadList();
      await selectSoul(name);
    } catch (err) {
      toast('Failed to create soul: ' + err.message, 'error');
    }
  }

  renderView();
}
