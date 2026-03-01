import { API } from '../api.js';
import { el, toast, confirmDialog, loading, emptyState, pagination, timeAgo } from '../components.js';

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

    const userInput = el('input', {
      className: 'input',
      placeholder: 'Filter by user',
      type: 'text',
      value: searchUserId,
    });
    const userGroup = el('div', { className: 'input-group' },
      el('label', { className: 'input-label' }, 'User ID'),
      userInput,
    );

    const queryInput = el('input', {
      className: 'input',
      placeholder: 'Search memories...',
      type: 'text',
      value: searchQuery,
    });
    const queryGroup = el('div', { className: 'input-group', style: { flex: '1', minWidth: '200px' } },
      el('label', { className: 'input-label' }, 'Search'),
      queryInput,
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

        // Normalize API PascalCase → local camelCase
        const content = mem.Content || mem.content || '';
        const userId = mem.UserID || mem.user_id || '';
        const createdAt = mem.CreatedAt || mem.created_at || '';
        const importance = mem.Importance ?? mem.importance ?? null;
        const memId = mem.ID || mem.id || '';

        // Content (expandable)
        const lines = content.split('\n');
        const isLong = lines.length > 3 || content.length > 200;
        const contentDiv = el('div', { className: 'memory-content expandable' + (isLong ? ' collapsed' : '') });
        contentDiv.textContent = content;
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
        if (userId) {
          meta.appendChild(el('span', {}, 'user: ' + userId));
        }
        if (createdAt) {
          const fullDate = new Date(createdAt).toLocaleString();
          meta.appendChild(el('span', { title: fullDate }, timeAgo(createdAt)));
        }
        if (importance != null) {
          const pct = Math.round(importance * 100);
          let importanceClass;
          if (importance >= 0.7) importanceClass = 'badge-amber';
          else if (importance >= 0.4) importanceClass = 'badge-warning';
          else importanceClass = 'badge-muted';
          meta.appendChild(el('span', { className: 'badge ' + importanceClass }, pct + '% importance'));
        }
        card.appendChild(meta);

        // Actions
        const actions = el('div', { className: 'memory-actions' });

        const memRef = { id: memId, content, importance, userId, createdAt };
        const editBtn = el('button', {
          className: 'btn btn-ghost btn-sm',
          onClick: () => openEditDialog(memRef),
        }, 'Edit');

        const deleteBtn = el('button', {
          className: 'btn btn-ghost btn-sm btn-danger',
          onClick: () => handleDelete(memRef),
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
    queryInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') handleSearch(); });
    userInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') handleSearch(); });

    function handleSearch() {
      searchQuery = queryInput.value.trim();
      searchUserId = userInput.value.trim();
      offset = 0;
      fetchMemories().then(() => renderView());
    }
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
    const textarea = el('textarea', {
      className: 'textarea',
      style: { minHeight: '120px' },
    });
    textarea.value = mem.content || '';

    const dialog = el('dialog', {},
      el('div', { className: 'dialog-title' }, 'Edit Memory'),
      el('div', { style: { marginBottom: 'var(--sp-4)' } }, textarea),
      el('div', { className: 'dialog-actions' },
        el('button', {
          className: 'btn btn-secondary',
          onClick: () => { dialog.close(); dialog.remove(); },
        }, 'Cancel'),
        el('button', {
          className: 'btn btn-primary',
          onClick: async () => {
            try {
              await API.patchMemory(mem.id, serverId, { content: textarea.value });
              toast('Memory updated', 'success');
              dialog.close();
              dialog.remove();
              await fetchMemories();
              renderView();
            } catch (err) {
              toast('Failed to update memory: ' + err.message, 'error');
            }
          },
        }, 'Save'),
      ),
    );

    document.body.appendChild(dialog);
    dialog.showModal();
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
