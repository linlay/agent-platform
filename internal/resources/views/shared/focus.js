// Installed by the builtin view service for read-only HTML templates.
(() => {
  'use strict';
  let state = null;
  // Controls that keep keyboard focus inside this iframe.
  const interactive = 'input,textarea,select,button,a[href],summary,label,[tabindex],[contenteditable]';
  addEventListener('message', event => {
    if (event.source !== parent) return;
    const message = event.data;
    if ((message?.type !== 'awaiting_init' && message?.type !== 'awaiting_update') || message.data?.mode !== 'form') return;
    state = message.data;
  });
  addEventListener('click', event => {
    if (!state || event.target.closest?.(interactive)) return;
    // A text selection must stay copyable from this document.
    const selection = getSelection();
    if (selection && !selection.isCollapsed) return;
    parent.postMessage({type: 'awaiting_focus_release', runId: state.runId, awaitingId: state.awaitingId}, '*');
  });
})();
