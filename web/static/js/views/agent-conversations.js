import { API } from '../api.js';
import { el, esc, toast, loading, emptyState, pagination, timeAgo } from '../components.js';

const PAGE_LIMIT = 20;

export async function render(container, params) {
  const agentId = params.id;
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);

  let currentOffset = 0;
  let channelFilter = '';

  // ── Controls ──
  const controls = el('div', { className: 'log-controls' });

  const filterInput = el('input', {
    type: 'text',
    className: 'code-editor',
    placeholder: 'Filter by channel ID...',
    style: { width: '240px', height: 'auto', padding: 'var(--sp-2) var(--sp-3)', minHeight: 'unset', resize: 'none' },
  });
  filterInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      channelFilter = filterInput.value.trim();
      currentOffset = 0;
      fetchConversations();
    }
  });
  controls.appendChild(filterInput);

  const refreshBtn = el('button', {
    className: 'btn btn-ghost btn-sm',
    type: 'button',
    onClick: () => {
      channelFilter = filterInput.value.trim();
      currentOffset = 0;
      fetchConversations();
    },
  }, 'Refresh');
  controls.appendChild(refreshBtn);

  wrap.appendChild(controls);

  // ── Cards container ──
  const cardsWrap = el('div');
  wrap.appendChild(cardsWrap);

  // ── Pagination container ──
  const pageWrap = el('div');
  wrap.appendChild(pageWrap);

  // ── Build conversation card ──
  function buildCard(conv) {
    const card = el('div', { className: 'card conv-card' });

    // Header: channel badge + timestamp
    const header = el('div', {
      style: { display: 'flex', alignItems: 'center', gap: 'var(--sp-2)', marginBottom: 'var(--sp-3)' },
    },
      el('span', { className: 'badge badge-lavender' }, esc(conv.channel_id || 'unknown')),
      el('span', {
        style: { fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' },
        title: conv.created_at || '',
      }, timeAgo(conv.created_at)),
    );
    card.appendChild(header);

    // User message
    const userMsg = el('div', { className: 'conv-msg user' },
      el('div', { className: 'conv-msg-label' }, 'User'),
      el('div', { style: { fontSize: 'var(--text-sm)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' } },
        esc(conv.user_message || ''),
      ),
    );
    card.appendChild(userMsg);

    // Tool calls (collapsible)
    if (conv.tool_calls && conv.tool_calls.length > 0) {
      const toolsDiv = el('div', { className: 'conv-tools' });

      const toolsSummary = el('div', {},
        'Tool calls (' + conv.tool_calls.length + ') - click to expand',
      );
      const toolsContent = el('pre', {
        style: { display: 'none', marginTop: 'var(--sp-2)', whiteSpace: 'pre-wrap', wordBreak: 'break-all' },
      }, JSON.stringify(conv.tool_calls, null, 2));

      toolsDiv.addEventListener('click', () => {
        const visible = toolsContent.style.display !== 'none';
        toolsContent.style.display = visible ? 'none' : 'block';
        toolsSummary.textContent = visible
          ? 'Tool calls (' + conv.tool_calls.length + ') - click to expand'
          : 'Tool calls (' + conv.tool_calls.length + ') - click to collapse';
      });

      toolsDiv.appendChild(toolsSummary);
      toolsDiv.appendChild(toolsContent);
      card.appendChild(toolsDiv);
    }

    // Bot response
    const botMsg = el('div', { className: 'conv-msg bot' },
      el('div', { className: 'conv-msg-label' }, 'Vespra'),
      el('div', { style: { fontSize: 'var(--text-sm)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' } },
        esc(conv.bot_response || ''),
      ),
    );
    card.appendChild(botMsg);

    return card;
  }

  // ── Fetch conversations ──
  async function fetchConversations() {
    cardsWrap.innerHTML = '';
    cardsWrap.appendChild(loading());
    pageWrap.innerHTML = '';

    const apiParams = { limit: PAGE_LIMIT, offset: currentOffset };
    if (channelFilter) apiParams.channel_id = channelFilter;

    try {
      const data = await API.getConversations(agentId, apiParams);
      const conversations = data.conversations || [];
      const total = data.total || 0;

      cardsWrap.innerHTML = '';

      if (!conversations.length) {
        cardsWrap.appendChild(emptyState('~', 'No conversations', 'No conversation entries found.'));
        return;
      }

      for (const conv of conversations) {
        cardsWrap.appendChild(buildCard(conv));
      }

      pageWrap.innerHTML = '';
      pageWrap.appendChild(pagination(total, currentOffset, PAGE_LIMIT, (newOffset) => {
        currentOffset = newOffset;
        fetchConversations();
      }));
    } catch (err) {
      cardsWrap.innerHTML = '';
      cardsWrap.appendChild(emptyState('!', 'Failed to load conversations', err.message));
      toast('Failed to load conversations: ' + err.message, 'error');
    }
  }

  // ── Initial load ──
  await fetchConversations();
}
