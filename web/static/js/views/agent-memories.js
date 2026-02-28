import { API } from '../api.js';
import { el, esc, toast, confirmDialog, loading, emptyState, pagination, timeAgo } from '../components.js';

const LIMIT = 25;

export async function render(container, params) {
  const agentId = params.id;
  container.innerHTML = '';
  container.appendChild(loading());

  let serverId = '';
  let searchQuery = '';
  let searchUserId = '';
  let offset = 0;
  let memories = [];
  let total = 0;

  // Resolve server_id from agent
  try {
    const agents = await API.listAgents();
    const agent = (agents || []).find(a => a.id === agentId || a.server_id === agentId);
    if (!agent) throw new Error('Agent not found');
    serverId = agent.server_id || '';
  } catch (err) {
    container.innerHTML = '';
    toast('Failed to load agent: ' + err.message, 'error');
    container.appendChild(emptyState('!', 'Failed to load agent', err.message));
    return;
  }

  await fetchMemories();

  function renderView() {
    container.innerHTML = '';
    const wrap = el('div', { className: 'fade-in' });

    // Search bar
    const searchBar = el('div', { style: { display: 'flex', gap: 'var(--sp-3)', marginBottom: 'var(--sp-6)', alignItems: 'flex-end', flexWrap: 'wrap' } });

    const userGroup = el('div', { className: 'input-group' },
      el('label', { className: 'input-label' }, 'User ID'),
      el('input', {
        className: 'input',
        placeholder: 'Filter by user',
        type: 'text',
        id: 'mem-user-id',
        value: searchUserId,
      }),
    );

    const queryGroup = el('div', { className: 'input-group', style: { flex: '1', minWidth: '200px' } },
      el('label', { className: 'input-label' }, 'Search'),
      el('input', {
        className: 'input',
        placeholder: 'Search memories...',
        type: 'text',
        id: 'mem-query',
        value: searchQuery,
      }),
    );

    const searchBtn = el('button', { className: 'btn btn-primary', onClick: () => handleSearch() }, 'Search');

    searchBar.append(userGroup, queryGroup, searchBtn);
    wrap.appendChild(searchBar);

    // Results
    if (memories.length === 0) {
      wrap.appendChild(emptyState('~', 'No memories found', total === 0 ? 'This agent has no stored memories yet.' : 'Try a different search.'));
    } else {
      const grid = el('div', { className: 'card-grid' });

      for (const mem of memories) {
        const card = el('div', { className: 'card memory-card' });

        // Content (expandable)
        const lines = (mem.content || '').split('\n');
        const isLong = lines.length > 3;
        const contentDiv = el('div', { className: 'memory-content expandable' + (isLong ? ' collapsed' : '') });
        contentDiv.textContent = mem.content || '';
        card.appendChild(contentDiv);

        if (isLong) {
          const toggle = el('div', { className: 'expand-toggle', onClick: () => {
            const collapsed = contentDiv.classList.toggle('collapsed');
            toggle.textContent = collapsed ? 'Show more' : 'Show less';
          } }, 'Show more');
          card.appendChild(toggle);
        }

        // Meta row
        const meta = el('div', { className: 'memory-meta' });
        if (mem.user_id) {
          meta.appendChild(el('span', {}, 'user: ' + mem.user_id));
        }
        if (mem.created_at) {
          meta.appendChild(el('span', {}, timeAgo(mem.created_at)));
        }
        if (mem.importance != null) {
          const importanceClass = mem.importance >= 0.7 ? 'badge-amber' : mem.importance >= 0.4 ? 'badge-warning' : 'badge-muted';
          meta.appendChild(el('span', { className: 'badge ' + importanceClass }, 'imp: ' + mem.importance));
        }
        card.appendChild(meta);

        // Actions
        const actions = el('div', { className: 'memory-actions' });

        const editBtn = el('button', {
          className: 'btn btn-ghost btn-sm',
          onClick: () => openEditDialog(mem),
        }, 'Edit');

        const deleteBtn = el('button', {
          className: 'btn btn-ghost btn-sm btn-danger',
          onClick: () => handleDelete(mem),
        }, 'Delete');

        actions.append(editBtn, deleteBtn);
        card.appendChild(actions);

        grid.appendChild(card);
      }

      wrap.appendChild(grid);

      // Pagination
      const pag = pagination(total, offset, LIMIT, async (newOffset) => {
        offset = newOffset;
        await fetchMemories();
        renderView();
      });
      wrap.appendChild(pag);
    }

    container.appendChild(wrap);

    // Wire up Enter key on search inputs
    const queryInput = document.getElementById('mem-query');
    const userInput = document.getElementById('mem-user-id');
    if (queryInput) queryInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') handleSearch(); });
    if (userInput) userInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') handleSearch(); });
  }

  async function handleSearch() {
    const queryInput = document.getElementById('mem-query');
    const userInput = document.getElementById('mem-user-id');
    searchQuery = queryInput ? queryInput.value.trim() : '';
    searchUserId = userInput ? userInput.value.trim() : '';
    offset = 0;
    await fetchMemories();
    renderView();
  }

  async function fetchMemories() {
    try {
      const params = { server_id: serverId, limit: LIMIT, offset };
      if (searchQuery) params.q = searchQuery;
      if (searchUserId) params.user_id = searchUserId;

      const data = await API.listMemories(params);
      memories = data.memories || [];
      total = data.total || 0;
    } catch (err) {
      toast('Failed to load memories: ' + err.message, 'error');
      memories = [];
      total = 0;
    }
  }

  function openEditDialog(mem) {
    const dialog = document.createElement('dialog');
    dialog.innerHTML = `
      <div class="dialog-title">Edit Memory</div>
      <div style="margin-bottom: var(--sp-4);">
        <textarea class="textarea" id="edit-mem-content" rows="6" style="min-height: 120px;">${esc(mem.content || '')}</textarea>
      </div>
      <div class="dialog-actions">
        <button class="btn btn-secondary" data-action="cancel">Cancel</button>
        <button class="btn btn-primary" data-action="save">Save</button>
      </div>
    `;
    document.body.appendChild(dialog);
    dialog.showModal();

    dialog.addEventListener('click', async (e) => {
      const action = e.target.dataset.action;
      if (action === 'cancel') {
        dialog.close();
        dialog.remove();
      }
      if (action === 'save') {
        const textarea = document.getElementById('edit-mem-content');
        const content = textarea ? textarea.value : '';
        try {
          await API.patchMemory(mem.id, serverId, { content });
          toast('Memory updated', 'success');
          dialog.close();
          dialog.remove();
          await fetchMemories();
          renderView();
        } catch (err) {
          toast('Failed to update memory: ' + err.message, 'error');
        }
      }
    });

    dialog.addEventListener('close', () => dialog.remove());
  }

  async function handleDelete(mem) {
    const preview = (mem.content || '').slice(0, 80);
    const ok = await confirmDialog('Delete Memory', `Delete this memory? "${preview}..."`);
    if (!ok) return;
    try {
      await API.deleteMemory(mem.id, serverId);
      toast('Memory deleted', 'success');
      await fetchMemories();
      renderView();
    } catch (err) {
      toast('Failed to delete memory: ' + err.message, 'error');
    }
  }

  renderView();
}
