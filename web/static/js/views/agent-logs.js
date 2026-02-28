import { API } from '../api.js';
import { el, toast, loading, emptyState, pagination, timeAgo } from '../components.js';

const LEVELS = ['all', 'debug', 'info', 'warn', 'error'];
const LEVEL_BADGES = {
  debug: 'badge badge-muted',
  info:  'badge badge-lavender',
  warn:  'badge badge-warning',
  error: 'badge badge-danger',
};
const PAGE_LIMIT = 100;
const POLL_INTERVAL = 3000;
const POLL_LIMIT = 20;

export async function render(container, params) {
  const agentId = params.id;
  const wrap = el('div', { className: 'fade-in' });
  container.appendChild(wrap);

  let currentLevel = 'all';
  let currentOffset = 0;
  let liveTail = false;
  let pollTimer = null;
  let seenIds = new Set();

  // ── Controls ──
  const controls = el('div', { className: 'log-controls' });

  // Level filter (mode-picker style)
  const levelPicker = el('div', { className: 'mode-picker' });
  function renderLevelPicker() {
    levelPicker.innerHTML = '';
    for (const lvl of LEVELS) {
      const btn = el('button', {
        className: 'mode-picker-btn' + (currentLevel === lvl ? ' active' : ''),
        type: 'button',
        onClick: () => {
          currentLevel = lvl;
          currentOffset = 0;
          renderLevelPicker();
          fetchLogs();
        },
      }, lvl);
      levelPicker.appendChild(btn);
    }
  }
  renderLevelPicker();
  controls.appendChild(levelPicker);

  // Live tail toggle
  const liveBtn = el('button', {
    className: 'btn btn-ghost btn-sm',
    type: 'button',
    onClick: () => {
      liveTail = !liveTail;
      updateLiveBtn();
      if (liveTail) {
        currentOffset = 0;
        startPolling();
      } else {
        stopPolling();
        fetchLogs();
      }
    },
  });
  const liveIndicator = el('span', { className: 'live-indicator', style: { display: 'none' } },
    el('span', { className: 'pulse' }),
    'LIVE',
  );
  controls.appendChild(liveBtn);
  controls.appendChild(liveIndicator);

  function updateLiveBtn() {
    liveBtn.textContent = liveTail ? 'Stop' : 'Live Tail';
    liveIndicator.style.display = liveTail ? 'inline-flex' : 'none';
  }
  updateLiveBtn();

  // Refresh button
  const refreshBtn = el('button', {
    className: 'btn btn-ghost btn-sm',
    type: 'button',
    onClick: () => fetchLogs(),
  }, 'Refresh');
  controls.appendChild(refreshBtn);

  wrap.appendChild(controls);

  // ── Table container ──
  const tableWrap = el('div');
  wrap.appendChild(tableWrap);

  // ── Pagination container ──
  const pageWrap = el('div');
  wrap.appendChild(pageWrap);

  // ── Build table ──
  function buildTable(logs) {
    const table = el('table', { style: { width: '100%', borderCollapse: 'collapse' } });

    const thead = el('thead', {},
      el('tr', {},
        el('th', {}, 'Time'),
        el('th', {}, 'Level'),
        el('th', {}, 'Message'),
        el('th', {}, 'Channel'),
      ),
    );
    table.appendChild(thead);

    const tbody = el('tbody');
    for (const log of logs) {
      tbody.appendChild(buildLogRow(log));
    }
    table.appendChild(tbody);
    return table;
  }

  // ── Fetch logs ──
  async function fetchLogs() {
    tableWrap.innerHTML = '';
    tableWrap.appendChild(loading());
    pageWrap.innerHTML = '';

    const apiParams = { limit: PAGE_LIMIT, offset: currentOffset };
    if (currentLevel !== 'all') apiParams.level = currentLevel;

    try {
      const data = await API.getLogs(agentId, apiParams);
      const logs = data.logs || [];
      const total = data.total || 0;

      tableWrap.innerHTML = '';

      if (!logs.length) {
        tableWrap.appendChild(emptyState('~', 'No logs', 'No log entries found for this agent.'));
        return;
      }

      seenIds = new Set(logs.map(l => l.id));
      tableWrap.appendChild(buildTable(logs));

      // Pagination (only when not in live tail mode)
      pageWrap.innerHTML = '';
      if (!liveTail) {
        pageWrap.appendChild(pagination(total, currentOffset, PAGE_LIMIT, (newOffset) => {
          currentOffset = newOffset;
          fetchLogs();
        }));
      }
    } catch (err) {
      tableWrap.innerHTML = '';
      tableWrap.appendChild(emptyState('!', 'Failed to load logs', err.message));
      toast('Failed to load logs: ' + err.message, 'error');
    }
  }

  // ── Live tail polling ──
  function startPolling() {
    stopPolling();
    fetchLogs();
    pollTimer = setInterval(pollNewLogs, POLL_INTERVAL);
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  async function pollNewLogs() {
    if (!liveTail) return;

    const apiParams = { limit: POLL_LIMIT, offset: 0 };
    if (currentLevel !== 'all') apiParams.level = currentLevel;

    try {
      const data = await API.getLogs(agentId, apiParams);
      const logs = data.logs || [];

      const newLogs = logs.filter(l => !seenIds.has(l.id));
      if (!newLogs.length) return;

      for (const l of newLogs) seenIds.add(l.id);

      // Prepend new rows to existing table
      const tbody = tableWrap.querySelector('tbody');
      if (!tbody) {
        // Table did not exist yet, full re-render
        tableWrap.innerHTML = '';
        seenIds = new Set(logs.map(l => l.id));
        tableWrap.appendChild(buildTable(logs));
        return;
      }

      const fragment = document.createDocumentFragment();
      for (const log of newLogs) {
        const row = buildLogRow(log);
        fragment.appendChild(row);
      }
      tbody.insertBefore(fragment, tbody.firstChild);
    } catch (err) {
      // Silently ignore polling errors
    }
  }

  function buildLogRow(log) {
    const timestamp = log.created_at || log.ts || '';
    const lvl = (log.level || '').toLowerCase();
    const msg = log.msg || log.message || '';
    const badgeClass = LEVEL_BADGES[lvl] || 'badge badge-muted';

    // Parse attrs whether string or object
    let attrs = log.attrs;
    if (typeof attrs === 'string') {
      try { attrs = JSON.parse(attrs); } catch { attrs = null; }
    }

    const row = el('tr', { style: { borderBottom: '1px solid var(--night-border)' } });

    row.appendChild(el('td', {
      style: { padding: 'var(--sp-2)', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', whiteSpace: 'nowrap' },
      title: timestamp,
    }, timeAgo(timestamp)));

    row.appendChild(el('td', { style: { padding: 'var(--sp-2)' } },
      el('span', { className: badgeClass }, lvl),
    ));

    const msgCell = el('td', { style: { padding: 'var(--sp-2)', maxWidth: '400px' } });
    msgCell.appendChild(el('div', {
      style: { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '400px', fontSize: 'var(--text-sm)' },
      title: msg,
    }, msg));

    if (attrs && Object.keys(attrs).length > 0) {
      const attrsToggle = el('div', {
        style: { fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)', cursor: 'pointer', marginTop: 'var(--sp-1)' },
      }, '+ attrs');
      const attrsContent = el('pre', {
        style: { display: 'none', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)', marginTop: 'var(--sp-1)', whiteSpace: 'pre-wrap', wordBreak: 'break-all' },
      }, JSON.stringify(attrs, null, 2));

      attrsToggle.addEventListener('click', () => {
        const visible = attrsContent.style.display !== 'none';
        attrsContent.style.display = visible ? 'none' : 'block';
        attrsToggle.textContent = visible ? '+ attrs' : '- attrs';
      });

      msgCell.appendChild(attrsToggle);
      msgCell.appendChild(attrsContent);
    }
    row.appendChild(msgCell);

    row.appendChild(el('td', {
      style: { padding: 'var(--sp-2)', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--cream-muted)' },
    }, log.channel_id || ''));

    return row;
  }

  // ── Cleanup on navigation ──
  const observer = new MutationObserver(() => {
    if (!container.contains(wrap)) {
      stopPolling();
      observer.disconnect();
    }
  });
  observer.observe(container, { childList: true });

  // ── Initial load ──
  await fetchLogs();
}
