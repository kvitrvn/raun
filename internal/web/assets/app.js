// Progressive enhancements of the knowledge browser. Every page and form works
// without this file: links, anchors, GET filters and POST decisions.
(() => {
  'use strict';
  const root = document.documentElement;
  root.setAttribute('data-enhanced', '');
  const apple = /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent);
  const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const ease = 'cubic-bezier(.2,.8,.2,1)';
  const $ = (selector, scope = document) => scope.querySelector(selector);
  const $$ = (selector, scope = document) => Array.from(scope.querySelectorAll(selector));
  const animate = (el, frames, options) => {
    if (el && !reducedMotion()) el.animate(frames, options);
  };
  const toastKey = 'raun:toast';

  // ----- page lifecycle ------------------------------------------------------

  let previous = null; // state of the page replaced by the last swap

  function snapshot() {
    const list = $('[data-list-scroll]');
    const detail = $('[data-detail-scroll]');
    const item = $('[data-detail]');
    return {
      listTop: list ? list.scrollTop : 0,
      detailTop: detail ? detail.scrollTop : 0,
      item: item ? item.getAttribute('data-detail') : '',
      progress: $$('[data-progress] [data-status]').map((s) => [s.getAttribute('data-status'), s.getAttribute('data-grow')]),
    };
  }

  function setup(swapped) {
    $$('[data-mod-key]').forEach((key) => { if (!apple) key.textContent = 'Ctrl+' + key.getAttribute('data-mod-key'); });
    const validate = $('[data-action=validate]');
    if (validate && !apple && !$('[data-decision][data-blocked]')) validate.title = 'Validate proposal (Ctrl+↵)';
    $$('[data-copy]').forEach((el) => {
      el.setAttribute('role', 'button');
      el.setAttribute('tabindex', '0');
      el.setAttribute('aria-label', el.getAttribute('data-copy-label') || 'Copy');
    });
    const trigger = $('[data-slot=sidebar-trigger]');
    if (trigger) {
      trigger.setAttribute('role', 'button');
      trigger.setAttribute('aria-expanded', String(sidebarOpen()));
    }
    const form = $('[data-decision]');
    if (form) form.noValidate = true;

    const list = $('[data-list-scroll]');
    const detail = $('[data-detail-scroll]');
    const item = $('[data-detail]');
    const itemID = item ? item.getAttribute('data-detail') : '';
    if (swapped && previous) {
      if (list) list.scrollTop = previous.listTop;
      if (detail && itemID && itemID === previous.item) detail.scrollTop = previous.detailTop;
      if (item && itemID !== previous.item) {
        animate(item, [{ opacity: 0, transform: 'translateY(8px)' }, { opacity: 1, transform: 'none' }], { duration: 260, easing: ease });
      }
    }
    revealRow(swapped && previous && itemID !== previous.item);
    spy();
    showStashedToast();
  }

  function announce() {
    const status = $('#navigation-status');
    const error = $('#error-summary');
    const active = document.activeElement;
    // Keep focus restored by htmx (search field, list row); otherwise move it.
    if (error) error.focus();
    else if (!active || active === document.body || !document.contains(active)) {
      const main = $('#main');
      if (main) main.focus({ preventScroll: true });
    }
    if (status) status.textContent = document.title.split(' · ')[0];
  }

  document.addEventListener('htmx:beforeSwap', () => {
    previous = snapshot();
    hideHoverCard(true);
  });
  document.addEventListener('htmx:afterSettle', () => {
    closeSidebar();
    setup(true);
    announce();
  });
  document.addEventListener('htmx:sendError', () => {
    $('#navigation-status').textContent = 'Cannot reach Raun. Check that the server is running, then reload the page.';
    $$('[data-busy]').forEach(resetBusy);
  });
  // A browser back-forward cache must not bypass a fresh store read.
  window.addEventListener('pageshow', (event) => {
    if (event.persisted) window.location.reload();
  });

  // ----- list ----------------------------------------------------------------

  function revealRow(smooth) {
    const list = $('[data-list-scroll]');
    const row = list && $('[data-row][aria-current]', list);
    if (!row) return;
    const top = row.offsetTop - list.offsetTop;
    const bottom = top + row.offsetHeight;
    const behavior = smooth && !reducedMotion() ? 'smooth' : 'auto';
    if (top < list.scrollTop) list.scrollTo({ top: top - 6, behavior });
    else if (bottom > list.scrollTop + list.clientHeight) list.scrollTo({ top: bottom - list.clientHeight + 6, behavior });
  }

  function move(direction) {
    const rows = $$('[data-row]');
    if (!rows.length) return;
    const current = rows.findIndex((row) => row.hasAttribute('aria-current'));
    const next = current < 0 ? 0 : Math.max(0, Math.min(rows.length - 1, current + direction));
    if (next === current) return;
    // Select at once; the navigation follows.
    rows.forEach((row) => row.removeAttribute('aria-current'));
    rows[next].setAttribute('aria-current', 'page');
    revealRow(true);
    rows[next].click();
  }

  // ----- detail sections -----------------------------------------------------

  let spyLock = 0;

  function setActiveSection(key) {
    $$('[data-section]').forEach((tab) => {
      const active = tab.getAttribute('data-section') === key;
      tab.toggleAttribute('data-active', active);
      if (active) tab.setAttribute('aria-current', 'location');
      else tab.removeAttribute('aria-current');
    });
  }

  function spy() {
    const scroller = $('[data-detail-scroll]');
    if (!scroller || Date.now() - spyLock < 600) return;
    let current = 'overview';
    for (const tab of $$('[data-section]')) {
      const key = tab.getAttribute('data-section');
      const section = document.getElementById('sec-' + key);
      if (section && section.offsetTop - 120 <= scroller.scrollTop) current = key;
    }
    if (scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 4) current = 'history';
    setActiveSection(current);
  }

  function scrollDetailTo(el, offset) {
    const scroller = $('[data-detail-scroll]');
    if (!scroller || !el) return;
    spyLock = Date.now();
    scroller.scrollTo({ top: Math.max(0, el.offsetTop - offset), behavior: reducedMotion() ? 'auto' : 'smooth' });
  }

  function goSection(key) {
    scrollDetailTo(document.getElementById('sec-' + key), 44);
    setActiveSection(key);
  }

  function flashEvidence(id) {
    const el = document.getElementById('evidence-' + id);
    if (!el) return;
    hideHoverCard(true);
    scrollDetailTo(el, 60);
    setActiveSection('evidence');
    el.focus({ preventScroll: true });
    setTimeout(() => animate(el, [
      { boxShadow: '0 0 0 0 oklch(0.44 0.07 175 / 0)' },
      { boxShadow: '0 0 0 4px oklch(0.44 0.07 175 / .25)' },
      { boxShadow: '0 0 0 0 oklch(0.44 0.07 175 / 0)' },
    ], { duration: 1100, easing: 'ease-out' }), 280);
  }

  document.addEventListener('scroll', (event) => {
    if (event.target.matches && event.target.matches('[data-detail-scroll]')) spy();
  }, true);

  // ----- citation hover cards ------------------------------------------------

  let hoverTimer = 0;

  function buildHoverCard(card, evidence) {
    const header = document.createElement('div');
    header.className = 'hover-card-header';
    const id = document.createElement('span');
    id.className = 'hover-card-id';
    id.textContent = $('.evidence-id', evidence).textContent;
    const path = document.createElement('span');
    path.className = 'hover-card-path';
    path.textContent = $('.evidence-path', evidence).firstChild.textContent;
    const range = document.createElement('span');
    range.className = 'evidence-range';
    range.textContent = $('.evidence-range', evidence).textContent;
    path.append(range);
    const spacer = document.createElement('span');
    spacer.className = 'detail-spacer';
    const badge = $('[data-state]', evidence);
    const state = document.createElement('span');
    state.className = 'hover-card-state tone-' + badge.getAttribute('data-state');
    state.textContent = badge.getAttribute('data-state');
    header.append(id, path, spacer, state);
    const pre = document.createElement('pre');
    pre.className = 'hover-card-excerpt';
    $$('.excerpt-line', evidence).slice(0, 6).forEach((line) => pre.append(line.cloneNode(true)));
    card.replaceChildren(header, pre);
  }

  function showHoverCard(cite) {
    const card = $('#evidence-preview');
    const evidence = document.getElementById('evidence-' + cite.getAttribute('data-cite'));
    if (!card || !evidence) return;
    buildHoverCard(card, evidence);
    const r = cite.getBoundingClientRect();
    const below = r.bottom + 8;
    card.style.left = Math.max(8, Math.min(r.left, window.innerWidth - 456)) + 'px';
    card.style.top = (below + 200 > window.innerHeight ? r.top - 208 : below) + 'px';
    card.hidden = false;
  }

  function hideHoverCard(now) {
    clearTimeout(hoverTimer);
    const card = $('#evidence-preview');
    if (!card) return;
    if (now) card.hidden = true;
    else hoverTimer = setTimeout(() => { card.hidden = true; }, 160);
  }

  document.addEventListener('mouseover', (event) => {
    const cite = event.target.closest && event.target.closest('[data-cite]');
    if (cite && !cite.contains(event.relatedTarget)) {
      clearTimeout(hoverTimer);
      hoverTimer = setTimeout(() => showHoverCard(cite), 220);
    } else if (event.target.closest && event.target.closest('#evidence-preview')) {
      clearTimeout(hoverTimer);
    }
  });
  document.addEventListener('mouseout', (event) => {
    const from = event.target.closest && event.target.closest('[data-cite], #evidence-preview');
    if (from && !from.contains(event.relatedTarget)) hideHoverCard(false);
  });
  document.addEventListener('focusin', (event) => {
    const cite = event.target.closest && event.target.closest('[data-cite]');
    if (cite) showHoverCard(cite);
  });
  document.addEventListener('focusout', (event) => {
    if (event.target.closest && event.target.closest('[data-cite]')) hideHoverCard(true);
  });

  // ----- copy ----------------------------------------------------------------

  const copyTimers = new WeakMap();

  function copy(el) {
    const text = el.getAttribute('data-copy');
    if (navigator.clipboard) navigator.clipboard.writeText(text).catch(() => {});
    el.setAttribute('data-copied', '');
    clearTimeout(copyTimers.get(el));
    copyTimers.set(el, setTimeout(() => el.removeAttribute('data-copied'), 1400));
  }

  // ----- sidebar -------------------------------------------------------------

  const desktop = () => window.matchMedia('(min-width: 1180px)').matches;

  function sidebarOpen() {
    return desktop() ? root.getAttribute('data-sidebar') !== 'collapsed' : root.getAttribute('data-sidebar') === 'open';
  }

  function toggleSidebar() {
    if (desktop()) {
      if (root.getAttribute('data-sidebar') === 'collapsed') root.removeAttribute('data-sidebar');
      else root.setAttribute('data-sidebar', 'collapsed');
    } else if (root.getAttribute('data-sidebar') === 'open') {
      closeSidebar();
    } else {
      root.setAttribute('data-sidebar', 'open');
    }
    const trigger = $('[data-slot=sidebar-trigger]');
    if (trigger) trigger.setAttribute('aria-expanded', String(sidebarOpen()));
  }

  function closeSidebar() {
    if (root.getAttribute('data-sidebar') === 'open') root.removeAttribute('data-sidebar');
    if (location.hash === '#sidebar') history.replaceState(history.state, '', location.pathname + location.search);
  }

  // ----- command palette -----------------------------------------------------

  let paletteIndex = 0;

  const paletteItems = () => $$('#command [data-slot=command-item]:not([hidden])');

  function selectPaletteItem(index) {
    const items = paletteItems();
    paletteIndex = Math.max(0, Math.min(items.length - 1, index));
    const input = $('#command [data-slot=command-input]');
    items.forEach((item, i) => {
      item.setAttribute('aria-selected', String(i === paletteIndex));
      if (!item.id) item.id = 'command-item-' + i;
    });
    const selected = items[paletteIndex];
    if (selected) {
      input.setAttribute('aria-activedescendant', selected.id);
      selected.scrollIntoView({ block: 'nearest' });
    } else {
      input.removeAttribute('aria-activedescendant');
    }
  }

  function filterPalette() {
    const dialog = $('#command');
    const query = $('[data-slot=command-input]', dialog).value.trim().toLowerCase();
    $$('[data-slot=command-item]', dialog).forEach((item) => {
      item.hidden = query !== '' && !item.getAttribute('data-value').toLowerCase().includes(query);
    });
    $$('[data-slot=command-group]', dialog).forEach((group) => {
      group.hidden = !$('[data-slot=command-item]:not([hidden])', group);
    });
    const empty = $('[data-slot=command-empty]', dialog);
    empty.hidden = paletteItems().length > 0;
    empty.textContent = 'No knowledge matches “' + query + '”.';
    selectPaletteItem(0);
  }

  function openPalette() {
    const dialog = $('#command');
    if (!dialog || dialog.open) return;
    hideHoverCard(true);
    const input = $('[data-slot=command-input]', dialog);
    input.value = '';
    filterPalette();
    dialog.showModal();
    input.focus();
    animate($('[data-slot=command]', dialog), [{ opacity: 0, transform: 'translateY(-6px) scale(.985)' }, { opacity: 1, transform: 'none' }], { duration: 180, easing: ease });
  }

  function closePalette() {
    const dialog = $('#command');
    if (dialog && dialog.open) dialog.close();
  }

  document.addEventListener('input', (event) => {
    if (event.target.matches('[data-slot=command-input]')) filterPalette();
    if (event.target.matches('.decision-reason, .decision-author-input')) setDecisionError('');
  });
  document.addEventListener('mousemove', (event) => {
    const item = event.target.closest && event.target.closest('#command [data-slot=command-item]');
    if (item) {
      const index = paletteItems().indexOf(item);
      if (index !== paletteIndex) selectPaletteItem(index);
    }
  });

  // ----- decisions -----------------------------------------------------------

  const blockedMessage = 'Validation is blocked by an unresolved disagreement. Rejection remains available.';

  function setDecisionError(message) {
    const el = $('[data-decision-error]');
    if (!el) return;
    el.textContent = message;
    el.hidden = message === '';
    const form = $('[data-decision]');
    if (form) form.toggleAttribute('data-invalid', message !== '' || !!$('#error-summary', form));
  }

  function resetBusy(button) {
    button.removeAttribute('data-busy');
    const spinner = $('[data-slot=spinner]', button);
    if (spinner) spinner.hidden = true;
    $$('[data-decision] [data-action]').forEach((b) => { b.disabled = b.hasAttribute('data-was-disabled'); });
  }

  function decide(action) {
    const form = $('[data-decision]');
    if (!form) return;
    if (action === 'validate' && form.hasAttribute('data-blocked')) {
      setDecisionError(blockedMessage);
      $('.decision-reason', form).focus();
      return;
    }
    const button = $('[data-action=' + action + ']', form);
    if (button && !button.disabled) form.requestSubmit(button);
  }

  // Runs before htmx: an incomplete decision is never sent.
  document.addEventListener('submit', (event) => {
    const form = event.target;
    if (!form.matches('[data-decision]')) return;
    const submitter = event.submitter;
    const action = submitter ? submitter.getAttribute('data-action') : '';
    if (!action) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    const author = $('.decision-author-input', form);
    const reason = $('.decision-reason', form);
    let message = '';
    if (!author.value.trim()) message = 'Author is required.';
    else if (!reason.value.trim()) message = 'A reason is required to ' + action + '.';
    if (message) {
      event.preventDefault();
      event.stopImmediatePropagation();
      setDecisionError(message);
      animate(form, [{ transform: 'translateX(0)' }, { transform: 'translateX(-5px)' }, { transform: 'translateX(5px)' }, { transform: 'translateX(-3px)' }, { transform: 'translateX(0)' }], { duration: 320 });
      (author.value.trim() ? reason : author).focus();
      return;
    }
    setDecisionError('');
    submitter.setAttribute('data-busy', '');
    const spinner = $('[data-slot=spinner]', submitter);
    if (spinner) spinner.hidden = false;
    form.setAttribute('data-submitted', action);
    // Disable after htmx has read the submitter.
    setTimeout(() => {
      $$('[data-action]', form).forEach((b) => {
        b.toggleAttribute('data-was-disabled', b.disabled);
        b.disabled = true;
      });
    }, 0);
  }, true);

  // A decision answered by HX-Redirect leaves the page: remember the toast.
  document.addEventListener('htmx:beforeOnLoad', (event) => {
    const form = event.detail.elt && event.detail.elt.closest && event.detail.elt.closest('[data-decision]');
    const xhr = event.detail.xhr;
    if (!form || !xhr || !xhr.getResponseHeader('HX-Redirect')) return;
    const validated = form.getAttribute('data-submitted') === 'validate';
    const toast = {
      kind: validated ? 'validated' : 'rejected',
      title: (validated ? 'Validated' : 'Rejected') + ' · ' + form.getAttribute('data-title'),
      text: 'Signed by ' + $('.decision-author-input', form).value.trim() + ' and recorded in history.',
      progress: snapshot().progress,
    };
    try { sessionStorage.setItem(toastKey, JSON.stringify(toast)); } catch { /* No toast without storage. */ }
  });

  function showStashedToast() {
    let toast = null;
    try {
      toast = JSON.parse(sessionStorage.getItem(toastKey) || 'null');
      sessionStorage.removeItem(toastKey);
    } catch { return; }
    if (!toast) return;
    animateProgress(toast.progress || []);
    const viewport = $('[data-slot=toast-viewport]');
    const template = viewport && $('[data-slot=toast-template]', viewport);
    if (!template) return;
    const el = template.content.firstElementChild.cloneNode(true);
    $$('[data-slot=toast-icon]', el).forEach((icon) => { if (icon.getAttribute('data-tone') !== toast.kind) icon.remove(); });
    $('[data-slot=toast-title]', el).textContent = toast.title;
    $('[data-slot=toast-description]', el).textContent = toast.text;
    viewport.append(el);
    animate(el, [{ opacity: 0, transform: 'translateY(-8px) scale(.98)' }, { opacity: 1, transform: 'none' }], { duration: 240, easing: ease });
    setTimeout(() => el.remove(), 3600);
  }

  // Grow the progress bar from the counts shown before the decision.
  function animateProgress(before) {
    if (reducedMotion() || !before.length) return;
    const old = new Map(before);
    const segments = $$('[data-progress] [data-status]');
    segments.forEach((s) => { s.style.flexGrow = old.get(s.getAttribute('data-status')) || '0'; });
    void document.body.offsetWidth;
    requestAnimationFrame(() => segments.forEach((s) => { s.style.flexGrow = ''; }));
  }

  // ----- clicks and keys -----------------------------------------------------

  document.addEventListener('click', (event) => {
    const target = event.target.closest ? event.target : event.target.parentElement;
    if (!target) return;
    const copyable = target.closest('[data-copy]');
    if (copyable) {
      event.preventDefault();
      copy(copyable);
      return;
    }
    if (target.closest('[data-slot=sidebar-trigger]')) {
      event.preventDefault();
      toggleSidebar();
      return;
    }
    if (target.closest('[data-slot=sheet-overlay]')) {
      event.preventDefault();
      closeSidebar();
      return;
    }
    if (target.closest('[data-slot=command-trigger]')) {
      event.preventDefault();
      openPalette();
      return;
    }
    const dialog = $('#command');
    if (dialog && event.target === dialog) {
      closePalette();
      return;
    }
    if (target.closest('#command [data-slot=command-item]')) {
      closePalette();
      return;
    }
    const section = target.closest('[data-section]');
    if (section) {
      event.preventDefault();
      goSection(section.getAttribute('data-section'));
      return;
    }
    const cite = target.closest('[data-cite]');
    if (cite) {
      event.preventDefault();
      flashEvidence(cite.getAttribute('data-cite'));
      return;
    }
    if (target.closest('#decision-blocked')) {
      event.preventDefault();
      goSection('points');
    }
  });

  const typing = (el) => el && (el.isContentEditable || /^(input|textarea|select)$/i.test(el.tagName));

  document.addEventListener('keydown', (event) => {
    const mod = event.metaKey || event.ctrlKey;
    const key = event.key;
    if (mod && key.toLowerCase() === 'k') {
      event.preventDefault();
      const dialog = $('#command');
      if (dialog && dialog.open) closePalette();
      else openPalette();
      return;
    }
    const dialog = $('#command');
    if (dialog && dialog.open) {
      if (key === 'ArrowDown') { event.preventDefault(); selectPaletteItem(paletteIndex + 1); }
      else if (key === 'ArrowUp') { event.preventDefault(); selectPaletteItem(paletteIndex - 1); }
      else if (key === 'Enter') {
        event.preventDefault();
        const item = paletteItems()[paletteIndex];
        if (item) item.click();
      }
      return;
    }
    if (key === 'Escape') hideHoverCard(true);
    const copyable = event.target.closest && event.target.closest('[data-copy]');
    if (copyable && (key === 'Enter' || key === ' ')) {
      event.preventDefault();
      copy(copyable);
      return;
    }
    if (typing(event.target)) {
      if (mod && key === 'Enter' && event.target.matches('.decision-reason')) {
        event.preventDefault();
        decide('validate');
      } else if (key === 'Escape') {
        event.target.blur();
      }
      return;
    }
    if (key === 'Escape' && root.getAttribute('data-sidebar') === 'open') {
      closeSidebar();
      return;
    }
    if (mod || event.altKey) return;
    if (key === '/') {
      const search = $('#q');
      if (search) { event.preventDefault(); search.focus(); }
    } else if (key === 'j' || key === 'ArrowDown') {
      event.preventDefault();
      move(1);
    } else if (key === 'k' || key === 'ArrowUp') {
      event.preventDefault();
      move(-1);
    } else if (key === 'v' || key === 'x') {
      const form = $('[data-decision]');
      if (!form) return;
      event.preventDefault();
      const reason = $('.decision-reason', form);
      if (reason.value.trim()) {
        decide(key === 'v' ? 'validate' : 'reject');
      } else {
        reason.focus();
        setDecisionError(key === 'v' && form.hasAttribute('data-blocked') ? blockedMessage : '');
      }
    }
  });

  setup(false);
})();
