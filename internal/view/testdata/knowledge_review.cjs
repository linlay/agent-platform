const fs = require('node:fs');
const assert = require('node:assert/strict');
const {JSDOM} = require(process.argv[2]);
const pages = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
for (const action of ['create', 'update', 'delete']) {
 const dom = new JSDOM(pages[action==='delete'?'resource_delete_review':'platform_control_review'], {
  runScripts: 'dangerously', pretendToBeVisual: true,
  beforeParse(w) { w.ResizeObserver=class {observe(){} disconnect(){}}; }
 });
 const w=dom.window, doc=w.document, replies=[];
 w.postMessage=value=>replies.push(value);
 const form={action,resourceType:'knowledge_library',resourceKey:'library-123',baseRevision:action==='create'?'':'revision',
  before:action==='create'?'':JSON.stringify({name:'Documents',collections:[{name:'docs',sourcePath:'/host/old-source'}]}),
  after:action==='delete'?'':JSON.stringify({name:'Documents',collections:[{name:'docs',sourcePath:'/host/canonical-source'}]}),
  sourcePermissions:[{name:'docs',sourcePath:'/host/canonical-source',include:['**/*.md'],exclude:['private/**'],editable:false},
   {name:'<img src=x onerror=alert(1)>',sourcePath:'/host/editable-source',include:['**/*.txt'],exclude:[],editable:true}]};
 const send=(type,data)=>w.dispatchEvent(new w.MessageEvent('message',{source:w,data:{type,data}}));
 send('awaiting_init',{mode:'form',runId:'run',awaitingId:'wait',locale:'en-US',form:{data:form}});
 const text=doc.querySelector('.fields').textContent+doc.querySelector('h1').textContent;
 assert.match(text,/knowledge library/);
 assert.ok(!text.includes('[object Object]'));
 for (const expected of ['/host/canonical-source','/host/editable-source','**/*.md','private/**']) assert.ok(text.includes(expected), expected);
 const editable=[...doc.querySelectorAll('.row')].filter(row=>row.querySelector('.label')?.textContent==='Editable');
 assert.deepEqual(editable.map(row=>row.querySelector('.copy').textContent),['Off','On']);
 for (const row of doc.querySelectorAll('.fields .row')) {
  const label=row.querySelector('.label')?.textContent || '';
  if (/^Source directory|^include$|^exclude$|^Editable$/.test(label)) assert.equal(row.querySelector('.copy').classList.contains('collapsed'),false);
 }
 assert.equal(doc.querySelector('img'),null);
 if (action==='update') assert.ok(text.includes('/host/old-source'));
 if (action==='delete') assert.match(text,/source directories are retained/);
 send('awaiting_collect',{runId:'run',awaitingId:'wait',decision:'submit'});
 assert.equal(replies.at(-1).type,'frontend_awaiting_submit');
 assert.equal(replies.at(-1).param.decision,'approve');
 assert.deepEqual(JSON.parse(JSON.stringify(replies.at(-1).param.data)),form);
 dom.window.close();
}
console.log('Library creation/update/deletion reviews show canonical grants, boolean values, safe text and immutable approval data.');
