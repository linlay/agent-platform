
(() => {
  'use strict';
  const text = {
    en: { agents: 'Agents', connectors: 'Connectors', all: 'All', valid: 'Valid', invalid: 'Invalid', refresh: 'Refresh', prev: 'Previous', next: 'Next', key: 'Key', name: 'Name', mode: 'Mode', status: 'Status', capabilities: 'Components', none: 'Nothing to show', loading: 'Loading…', unavailable: 'Live data cannot be queried here.', total: '{n} total', invocable: 'invocable', builtin: 'built-in', editable: 'editable' },
    zh: { agents: '智能体', connectors: '连接器', all: '全部', valid: '有效', invalid: '无效', refresh: '刷新', prev: '上一页', next: '下一页', key: 'Key', name: '名称', mode: '类型', status: '状态', capabilities: '组件', none: '没有可显示的内容', loading: '加载中…', unavailable: '此处无法查询实时数据。', total: '共 {n} 个', invocable: '可调用', builtin: '内置', editable: '可编辑' },
  };
  const columns = {"agent": [["key", "resourceKey"], ["name", "name"], ["mode", "mode"]], "connector": [["key", "resourceKey"], ["capabilities", "components"]], "provider": [["key", "resourceKey"], ["protocols", "protocols"], ["modelCount", "modelCount"], ["defaultModel", "defaultModel"], ["credentials", "credentialConfigured"]], "model": [["key", "resourceKey"], ["name", "name"], ["provider", "provider"], ["modelType", "type"], ["modelId", "modelId"], ["protocol", "protocol"], ["contextWindow", "contextWindow"]], "skill": [["key", "resourceKey"], ["name", "name"], ["description", "description"]], "tool": [["key", "resourceKey"], ["description", "description"]], "mcp": [["key", "resourceKey"], ["connector", "connectorId"], ["transport", "transport"], ["enabled", "enabled"]]};
  Object.assign(text.en, {"agentTitle": "Agents", "connectorTitle": "Connectors", "providerTitle": "Providers", "modelTitle": "Models", "skillTitle": "Skills", "toolTitle": "Standalone tools", "mcpTitle": "MCP components", "agentNotice": "Public Agents, including TEAM Agents.", "connectorNotice": "Installed connectors and their declared components.", "providerNotice": "Local configuration only. Configured credentials do not confirm remote availability.", "modelNotice": "Loaded model definitions; remote availability has not been checked.", "skillNotice": "Skill-center members; excludes Agent-local and connector-owned Skills.", "toolNotice": "Standalone tool catalog; connector tools belong to their connectors.", "mcpNotice": "Local connector declarations, including disabled components; remote availability has not been checked.", "protocols": "Protocols", "modelCount": "Models", "defaultModel": "Default model", "credentials": "Credentials configured", "yes": "Yes", "no": "No", "provider": "Provider", "modelType": "Type", "modelId": "Model ID", "protocol": "Protocol", "contextWindow": "Context window", "description": "Description", "connector": "Connector", "transport": "Transport", "enabled": "Enabled"});
  Object.assign(text.zh, {"agentTitle": "智能体", "connectorTitle": "连接器", "providerTitle": "模型提供商", "modelTitle": "模型", "skillTitle": "技能", "toolTitle": "独立工具", "mcpTitle": "MCP 组件", "agentNotice": "公开智能体，包含 TEAM 智能体。", "connectorNotice": "已安装的连接器及其声明组件。", "providerNotice": "仅反映本地配置；已配置凭据不代表远端服务可用。", "modelNotice": "已加载的模型定义；未检查远端可用性。", "skillNotice": "仅展示技能中心成员，不包含智能体私有技能或连接器自带技能。", "toolNotice": "独立工具目录；连接器工具归所属连接器展示。", "mcpNotice": "本地连接器声明，包含停用组件；未检查远端可用性。", "protocols": "协议", "modelCount": "模型数", "defaultModel": "默认模型", "credentials": "已配置凭据", "yes": "是", "no": "否", "provider": "提供商", "modelType": "类型", "modelId": "上游模型", "protocol": "协议", "contextWindow": "上下文窗口", "description": "说明", "connector": "连接器", "transport": "传输方式", "enabled": "已启用"});
  const resourceType = document.body.dataset.resourceType;
  let dict = text.zh;
  const t = (key, values) => (dict[key] || key).replace(/\{(\w+)\}/g, (_, name) => values?.[name] ?? '');
  const $ = (id) => document.getElementById(id);
  const el = (tag, className, content) => { const node = document.createElement(tag); if (className) node.className = className; if (content !== undefined) node.textContent = content; return node; };
  // cursors[i] is the cursor that loaded page i; page 0 has none.
  let data = null, params = { resourceType, status: 'all', limit: 20 }, cursors = [''], queryable = false, busy = false, input = null, failure = null;

  function applyText() { document.querySelectorAll('[data-i18n]').forEach((node) => { node.textContent = t(node.dataset.i18n); }); }



  function diagnostics(item) {
    return (Array.isArray(item.diagnostics) ? item.diagnostics : []).map((d) => d?.message || d?.code || JSON.stringify(d)).join('; ');
  }

  function render() {
    $('title').textContent = t(resourceType + 'Title');
    $('notice').textContent = t(resourceType + 'Notice');
    $('status').value = params.status || 'all';
    $('status').disabled = busy || !queryable;
    $('refresh').disabled = busy || !queryable;
    $('count').textContent = data ? t('total', { n: data.total ?? data.items.length }) : '';
    $('pager').hidden = true;
    const body = $('body');
    body.replaceChildren();
    if (failure) { body.append(el('div', 'error', String(failure?.message || failure))); }
    if (!data) { if (!failure) body.append(el('div', 'empty', t(queryable ? 'loading' : 'unavailable'))); return; }
    const items = data.items || [];
    if (!items.length) body.append(el('div', 'empty', t('none')));
    else {
      const table = el('table');
      const head = el('tr');
      columns[resourceType].forEach(([label]) => head.append(el('th', '', t(label))));
      head.append(el('th', '', t('status')));
      table.append(el('thead')); table.tHead.append(head);
      const rows = el('tbody');
      for (const item of items) {
        const row = el('tr');
        const value = { ...item, ...(item.definition || {}) };
        for (const [label, field] of columns[resourceType]) {
          const cell = el('td', label === 'key' ? 'key' : '');
          if (field === 'components') {
            [['hasNative', 'native'], ['hasMcp', 'MCP'], ['hasCli', 'CLI'], ['hasView', 'VIEW']].forEach(([key, name]) => { if (item[key]) cell.append(el('span', 'chip', name)); });
            if (item.editable === false) cell.append(el('span', 'chip', t('builtin')));
          } else {
            const content = value[field];
            cell.textContent = Array.isArray(content) ? (content.join(', ') || '—') : typeof content === 'boolean' ? t(content ? 'yes' : 'no') : (content ?? '—');
            if (resourceType === 'agent' && field === 'name' && (item.role || item.description)) cell.append(el('div', 'sub', item.role || item.description));
          }
          row.append(cell);
        }
        const status = el('td');
        status.append(el('div', item.valid ? 'ok' : 'bad', item.valid ? t('valid') : t('invalid')));
        if (resourceType === 'agent' && item.invocable) status.append(el('div', 'sub', t('invocable')));
        if (diagnostics(item)) status.append(el('div', 'sub', diagnostics(item)));
        row.append(status);
        rows.append(row);
      }
      table.append(rows);
      body.append(table);
    }
    const page = cursors.length - 1;
    $('pager').hidden = !queryable || (page === 0 && !data.hasMore);
    $('prev').disabled = busy || page === 0;
    $('next').disabled = busy || !data.hasMore;
  }


  async function load(next, nextCursors) {
    if (!queryable || busy) return;
    busy = true; render();
    const request = { ...next };
    const cursor = nextCursors[nextCursors.length - 1];
    if (cursor) request.cursor = cursor; else delete request.cursor;
    try {
      const { data: result } = await viewHost.query('list', request);
      params = next; cursors = nextCursors; data = result; failure = null;
    } catch (error) { failure = error; }
    busy = false; render();
  }

  // The block body is the view input; the first page is queried here.
  viewHost.onData((payload, _view, context) => {
    dict = String(context?.locale || navigator.language).toLowerCase().startsWith('zh') ? text.zh : text.en;
    applyText();
    const key = JSON.stringify(payload ?? null), nextQueryable = Boolean(context?.queryable);
    if (key === input && nextQueryable === queryable) { render(); return; } // locale/theme refresh only
    input = key; queryable = nextQueryable;
    const value = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
    data = null; failure = null;
    params = { resourceType, status: value.status || 'all', limit: value.limit || 20 }; cursors = [''];
    render();
    load(params, cursors);
  });
  $('status').addEventListener('change', () => load({ ...params, status: $('status').value }, ['']));
  $('refresh').addEventListener('click', () => load({ ...params }, cursors));
  $('prev').addEventListener('click', () => load({ ...params }, cursors.slice(0, -1)));
  $('next').addEventListener('click', () => load({ ...params }, [...cursors, data.nextCursor]));
  applyText();
  render();
})();
