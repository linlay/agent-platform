const Review = (() => {
  'use strict';
  const own = (o, k) => Object.prototype.hasOwnProperty.call(o || {}, k);
  const record = v => v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = String(text);
    return n;
  };
  function mount(render) {
    let state = null, closeDetails = () => {}, dispose = () => {};
    addEventListener('keydown', e => { if (e.key === 'Escape') closeDetails(true); });
    addEventListener('click', e => {
      if (!e.target.closest?.('.details-panel, .details-trigger')) closeDetails(false);
    });
    addEventListener('message', event => {
      if (event.source !== parent) return;
      const message = event.data;
      if (!message || typeof message !== 'object') return;
      if ((message.type === 'awaiting_init' || message.type === 'awaiting_update') && message.data?.mode === 'form') {
        state = null;
        dispose();
        const data = message.data, form = record(record(data.form).data), args = record(form.args);
        // Display helpers never mutate the form returned to the host.
        const current = {...record(form.current)};
        const zh = String(data.locale || navigator.language).startsWith('zh');
        const t = (cn, en) => zh ? cn : en;
        document.documentElement.lang = zh ? 'zh-CN' : 'en';
        if (data.colorScheme === 'dark' || data.colorScheme === 'light') document.documentElement.dataset.theme = data.colorScheme;
        else delete document.documentElement.dataset.theme;
        const main = node('main'), header = node('header', 'review-header'), heading = node('h1');
        const fields = node('section', 'fields'), notes = node('section', 'notes');
        const trigger = node('button', 'details-trigger', t('详情', 'Details'));
        trigger.type = 'button'; trigger.setAttribute('aria-expanded', 'false');
        trigger.setAttribute('aria-controls', 'review-details');
        const panel = node('section', 'details-panel'); panel.id = 'review-details'; panel.hidden = true;
        panel.setAttribute('role', 'region'); panel.setAttribute('aria-label', t('详情', 'Details'));
        const panelHeader = node('div', 'details-header'), close = node('button', 'text-button', t('关闭', 'Close'));
        close.type = 'button';
        const pre = node('pre', '', JSON.stringify(form, null, 2)); pre.tabIndex = 0;
        panelHeader.append(node('strong', '', t('详情', 'Details')), close); panel.append(panelHeader, pre);
        header.append(heading, trigger); main.append(header, fields, notes, panel);
        let serial = 0, hasCompare = false;
        const expanders = [];
        const format = v => v === undefined ? t('未知', 'Unknown') : v === null || v === '' ? t('未设置', 'Not set') :
          typeof v === 'boolean' ? (v ? t('开启', 'On') : t('关闭', 'Off')) : Array.isArray(v) ?
          (v.length ? v.map(format).join('\n') : t('无', 'None')) : typeof v === 'object' ?
          t('复合设置，见详情', 'Structured setting; see details') : String(v);
        function textBlock(text, {critical = false, target = false} = {}) {
          const box = node('div', target ? 'target-text' : 'text-block');
          const content = node('div', target ? 'target-copy' : 'copy', text);
          content.id = 'review-text-' + (++serial);
          box.append(content);
          if (!critical) {
            content.classList.add('collapsed');
            const button = node('button', 'text-button expand-button'); button.type = 'button'; button.hidden = true;
            button.setAttribute('aria-expanded', 'false'); button.setAttribute('aria-controls', content.id);
            const label = target ? t('完整名称', 'Full name') : t('展开全文 · ' + Array.from(String(text)).length + ' 字', 'Show full text · ' + Array.from(String(text)).length + ' characters');
            button.textContent = label;
            button.onclick = () => {
              const expanded = button.getAttribute('aria-expanded') !== 'true';
              content.classList.toggle('collapsed', !expanded); button.setAttribute('aria-expanded', String(expanded));
              button.textContent = expanded ? t('收起', 'Show less') : label;
            };
            box.append(button); expanders.push({content, button});
          }
          return box;
        }
        function measure() {
          for (const {content, button} of expanders) {
            if (button.getAttribute('aria-expanded') === 'false') button.hidden = content.scrollHeight <= content.clientHeight + 1;
          }
          if (!panel.hidden) {
            // Keep the floating panel inside this iframe, including short reviews.
            pre.style.maxHeight = '260px';
            main.style.minHeight = (panel.offsetTop + panel.offsetHeight) + 'px';
          }
        }
        closeDetails = restore => {
          if (panel.hidden) return;
          panel.hidden = true; trigger.setAttribute('aria-expanded', 'false'); main.style.minHeight = '';
          if (restore) trigger.focus();
        };
        trigger.onclick = () => {
          if (!panel.hidden) { closeDetails(true); return; }
          panel.hidden = false; trigger.setAttribute('aria-expanded', 'true'); measure(); close.focus();
        };
        close.onclick = () => closeDetails(true);
        const ui = {
          t, own, record, args, current, form, action: form.action, format,
          title(title) { heading.textContent = title; },
          target(name, _subtitle, id) {
            const text = String(name || id || t('未指定', 'Not specified')) + (id && !String(name || '').includes(id) ? ' · ' + id : '');
            const target = textBlock(text, {target: true}); target.classList.add('target');
            header.after(target);
          },
          field(label, value, options = {}) {
            const row = node('div', 'row'); row.append(node('div', 'label', label), textBlock(format(value), options)); fields.append(row);
          },
          compare(label, before, after, formatter = format, options = {}) {
            if (JSON.stringify(before) === JSON.stringify(after)) return;
            const oldText = formatter(before), newText = formatter(after);
            const long = !options.critical && !options.target && (String(oldText).length > 140 || String(newText).length > 140 || String(oldText).includes('\n') || String(newText).includes('\n'));
            if (!long && !hasCompare) {
              const legend = node('div', 'compare-row legend'); legend.setAttribute('aria-hidden', 'true');
              legend.append(node('div'), node('div', '', t('当前', 'Current')), node('div'), node('div', '', t('修改后', 'After')));
              fields.insertBefore(legend, fields.querySelector('.long-change')); hasCompare = true;
            }
            const row = node('div', long ? 'long-change' : 'compare-row row');
            row.append(node('div', 'label', label));
            const old = node('div', 'old'), next = node('div', 'new');
            old.append(node('span', 'caption', t('当前', 'Current')), textBlock(oldText, options));
            next.append(node('span', 'caption', t('修改后', 'After')), textBlock(newText, options));
            row.append(old, node('div', 'arrow', '→'), next);
            if (long) fields.append(row); else fields.insertBefore(row, fields.querySelector('.long-change'));
          },
          change(label, key, value, formatter = format, options = {}) {
            if (!own(current, key)) { this.field(label, formatter(value), options); return; }
            this.compare(label, current[key], value, formatter, options);
          },
          notice(text, warning = false) { notes.append(node('p', 'notice' + (warning ? ' warning' : ''), text)); },
          name(id, fallback) { return record(form.names)[id] || id || fallback || t('未指定', 'Not specified'); },
          extras(input, known) {
            const extra = Object.keys(record(input)).filter(k => !known.includes(k));
            if (extra.length) this.notice(t('另有 ' + extra.length + ' 项设置，见详情。', extra.length + ' additional settings; see details.'), true);
          },
          required(value, label) {
            if (value === undefined || value === null || value === '') this.notice(t('缺少' + label, 'Missing ' + label), true);
          }
        };
        try { render(ui); state = data; } catch {
          closeDetails = () => {};
          main.replaceChildren(node('p', 'notice warning', t('内容加载失败，请重新提交。', 'Unable to display this request. Resubmit it.')));
        }
        document.getElementById('review').replaceChildren(main);
        const observer = new ResizeObserver(measure);
        observer.observe(main);
        const pendingMeasure = requestAnimationFrame(measure);
        addEventListener('resize', measure);
        dispose = () => {
          observer.disconnect();
          cancelAnimationFrame(pendingMeasure);
          removeEventListener('resize', measure);
        };
      }
      if (message.type === 'awaiting_collect' && state && message.data?.runId === state.runId && message.data?.awaitingId === state.awaitingId && (message.data?.decision === 'submit' || message.data?.decision === 'reject')) {
        // The host asks for the current data on both approve and reject.
        const decision = message.data.decision === 'reject' ? 'reject' : 'approve';
        parent.postMessage({type: 'frontend_awaiting_submit', param: {decision, data: record(record(state.form).data)}}, '*');
      }
    });
  }
  return {mount};
})();
