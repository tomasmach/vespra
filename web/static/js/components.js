// Reusable UI components

/** Escape HTML */
export function esc(str) {
  const d = document.createElement('div');
  d.textContent = str ?? '';
  return d.innerHTML;
}

/** Create element shorthand */
export function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'className') e.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(e.style, v);
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2).toLowerCase(), v);
    else e.setAttribute(k, v);
  }
  for (const c of children) {
    if (c == null) continue;
    if (typeof c === 'string') e.appendChild(document.createTextNode(c));
    else e.appendChild(c);
  }
  return e;
}

/** Toast notifications */
const TOAST_DURATION = 4000;

export function toast(message, type = 'info') {
  const container = document.getElementById('toasts');
  if (!container) return;
  const t = el('div', { className: `toast ${type}` }, message);
  container.appendChild(t);
  setTimeout(() => {
    t.style.opacity = '0';
    t.style.transform = 'translateY(8px)';
    t.style.transition = 'all 0.25s';
    setTimeout(() => t.remove(), 250);
  }, TOAST_DURATION);
}

/** Mode picker: smart / mention / all / none */
export function modePicker(current, inherited, onChange) {
  const modes = ['smart', 'mention', 'all', 'none'];
  const wrap = el('div', { className: 'mode-picker' });

  function render() {
    wrap.innerHTML = '';
    for (const m of modes) {
      const isActive = current === m;
      const isInherited = !current && inherited === m;
      let cls = 'mode-picker-btn';
      if (isActive) cls += ' active';
      if (isInherited) cls += ' inherited';
      const btn = el('button', { className: cls, type: 'button' }, m);
      btn.addEventListener('click', () => {
        current = m;
        onChange(m);
        render();
      });
      wrap.appendChild(btn);
    }
  }

  render();
  return wrap;
}

/** Confirmation dialog */
export function confirmDialog(title, message) {
  return new Promise((resolve) => {
    const dialog = el('dialog');
    dialog.innerHTML = `
      <div class="dialog-title">${esc(title)}</div>
      <div class="dialog-body">${esc(message)}</div>
      <div class="dialog-actions">
        <button class="btn btn-secondary" data-action="cancel">Cancel</button>
        <button class="btn btn-danger" data-action="confirm">Confirm</button>
      </div>
    `;
    document.body.appendChild(dialog);
    dialog.showModal();

    dialog.addEventListener('click', (e) => {
      const action = e.target.dataset.action;
      if (action === 'confirm') { dialog.close(); dialog.remove(); resolve(true); }
      if (action === 'cancel') { dialog.close(); dialog.remove(); resolve(false); }
    });

    dialog.addEventListener('close', () => { dialog.remove(); resolve(false); });
  });
}

/** Pagination */
export function pagination(total, offset, limit, onPage) {
  const pages = Math.ceil(total / limit);
  const current = Math.floor(offset / limit) + 1;
  if (pages <= 1) return el('div');

  const wrap = el('div', { className: 'pagination' });

  const prev = el('button', {
    className: 'btn btn-ghost btn-sm',
    disabled: current <= 1 ? '' : undefined,
    onClick: () => onPage((current - 2) * limit),
  }, 'Prev');

  const info = el('span', {}, `Page ${current} of ${pages}`);

  const next = el('button', {
    className: 'btn btn-ghost btn-sm',
    disabled: current >= pages ? '' : undefined,
    onClick: () => onPage(current * limit),
  }, 'Next');

  wrap.append(prev, info, next);
  return wrap;
}

/** Section with mono label */
export function section(label, ...children) {
  const s = el('div', { className: 'section' });
  if (label) {
    s.appendChild(el('div', { className: 'section-header' },
      el('span', { className: 'mono-label' }, label)
    ));
  }
  for (const c of children) if (c) s.appendChild(c);
  return s;
}

/** Loading placeholder */
export function loading() {
  return el('div', { className: 'empty-state fade-in' },
    el('div', { className: 'empty-state-icon' }, '...'),
    el('div', { className: 'empty-state-title' }, 'Loading'),
  );
}

/** Empty state */
export function emptyState(icon, title, subtitle) {
  return el('div', { className: 'empty-state fade-in' },
    el('div', { className: 'empty-state-icon' }, icon),
    el('div', { className: 'empty-state-title' }, title),
    subtitle ? el('div', { style: { marginTop: '8px' } }, subtitle) : null,
  );
}

/** Relative time */
export function timeAgo(dateStr) {
  if (!dateStr) return '';
  const d = new Date(dateStr);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return 'just now';
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}
