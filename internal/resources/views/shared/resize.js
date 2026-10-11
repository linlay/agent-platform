// Installed by the builtin view service for every HTML template.
(() => {
  'use strict';
  let state = null, lastHeight = 0, pending = 0;
  function measure() {
    pending = 0;
    if (!state || !document.body) return;
    // scrollHeight is at least the current iframe height and prevents shrinking.
    const body = document.body;
    const marginBottom = parseFloat(getComputedStyle(body).marginBottom) || 0;
    const height = Math.ceil(body.getBoundingClientRect().bottom + scrollY + marginBottom);
    if (height <= 0 || height === lastHeight) return;
    lastHeight = height;
    parent.postMessage({type: 'awaiting_resize', runId: state.runId,
      awaitingId: state.awaitingId, formId: state.activeFormId || state.awaitingId, height}, '*');
  }
  function schedule() {
    if (!pending) pending = requestAnimationFrame(measure);
  }
  addEventListener('message', event => {
    if (event.source !== parent) return;
    const message = event.data;
    if ((message?.type !== 'awaiting_init' && message?.type !== 'awaiting_update') || message.data?.mode !== 'form') return;
    state = message.data;
    lastHeight = 0;
    schedule();
  });
  const observer = new ResizeObserver(schedule);
  observer.observe(document.body);
  addEventListener('resize', schedule);
  addEventListener('pagehide', () => {
    observer.disconnect();
    cancelAnimationFrame(pending);
  }, {once: true});
})();
