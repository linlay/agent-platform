const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { JSDOM } = require(process.argv[2]);
const root = process.argv[3];
const manifest = JSON.parse(fs.readFileSync(path.join(root, 'view.json'), 'utf8'));
const tick = () => new Promise(resolve => setImmediate(resolve));

(async () => {
  for (const [key, definition] of Object.entries(manifest.views)) {
    const type = definition.queries.list.paramsSchema.properties.resourceType.enum[0];
    const html = fs.readFileSync(path.join(root, definition.entry), 'utf8');
    const dom = new JSDOM(html, { runScripts: 'outside-only' });
    const w = dom.window, doc = w.document, calls = [];
    let receive, fail = false;
    const item = { resourceKey: 'demo', valid: true, name: '<img src=x onerror=alert(1)>', mode: 'TEAM', hasMcp: true, description: 'Description', definition: { provider: 'demo-provider', modelId: 'upstream', protocols: ['OPENAI'], modelCount: 0, credentialConfigured: false, enabled: false, transport: 'stdio' } };
    w.viewHost = {
      onData: fn => { receive = fn; },
      query: async (name, params) => {
        calls.push(JSON.parse(JSON.stringify({ name, params })));
        if (fail) throw new Error('Query failed');
        return { executedAt: 1, data: { items: [{...item, resourceKey: params.cursor ? 'second' : 'first'}], total: 2, hasMore: !params.cursor, nextCursor: params.cursor ? '' : 'page2' } };
      },
    };
    for (const asset of definition.assets.filter(p => p.endsWith('.js'))) w.eval(fs.readFileSync(path.join(root, asset), 'utf8'));
    const context = { locale: 'en-US', queryable: true, query: { params: { resourceType: type, limit: 1 } } };
    receive({ items: [item], total: 2, hasMore: true, nextCursor: 'page2' }, {}, context);
    assert.equal(doc.querySelectorAll('tbody tr').length, 1, key);
    assert.equal(doc.querySelectorAll('[data-type], .tabs, img').length, 0, key);
    assert.ok(doc.querySelector('#title').textContent.length, key);
    if (type === 'model') assert.match(doc.querySelector('tbody').textContent, /demo-provider.*upstream/);
    if (type === 'provider') assert.match(doc.querySelector('tbody').textContent, /OPENAI.*0.*No/);
    if (type === 'mcp') assert.match(doc.querySelector('tbody').textContent, /stdio.*No/);
    doc.querySelector('#next').click(); await tick();
    assert.deepEqual(calls.at(-1), { name: 'list', params: { resourceType: type, status: 'all', limit: 1, cursor: 'page2' } });
    assert.match(doc.querySelector('tbody').textContent, /second/);
    doc.querySelector('#prev').click(); await tick();
    assert.equal(calls.at(-1).params.cursor, undefined);
    const status = doc.querySelector('#status'); status.value = 'invalid'; status.dispatchEvent(new w.Event('change')); await tick();
    assert.equal(calls.at(-1).params.status, 'invalid');
    assert.equal(calls.at(-1).params.resourceType, type);
    fail = true; doc.querySelector('#refresh').click(); await tick();
    assert.match(doc.querySelector('.error').textContent, /Query failed/);
    assert.equal(doc.querySelector('#refresh').disabled, false);
    receive({ items: [], total: 0 }, {}, { locale: 'zh-CN', queryable: false });
    assert.match(doc.querySelector('#body').textContent, /没有可显示/);
    assert.equal(doc.querySelector('#pager').hidden, true);
    assert.equal(doc.querySelector('#refresh').disabled, true);
    dom.window.close();
  }
  console.log('Seven independent catalog views: fields, escaping, pagination, filters, errors and static rendering passed.');
})().catch(error => { console.error(error); process.exit(1); });
