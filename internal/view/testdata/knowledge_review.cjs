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
  sourcePermissions:[{name:'docs',sourcePath:'/host/canonical-source',include:['**/*.md'],exclude:['private/**']},
   {name:'<img src=x onerror=alert(1)>',sourcePath:'/host/second-source',include:['**/*.txt'],exclude:[]}]};
 const send=(type,data)=>w.dispatchEvent(new w.MessageEvent('message',{source:w,data:{type,data}}));
 send('awaiting_init',{mode:'form',runId:'run',awaitingId:'wait',locale:'en-US',form:{data:form}});
 const text=doc.querySelector('.fields').textContent+doc.querySelector('h1').textContent;
 assert.match(text,/knowledge library/);
 assert.ok(!text.includes('[object Object]'));
 for (const expected of ['/host/canonical-source','/host/second-source','**/*.md','private/**']) assert.ok(text.includes(expected), expected);
 const editable=[...doc.querySelectorAll('.row')].filter(row=>row.querySelector('.label')?.textContent==='Editable');
 assert.equal(editable.length, 0);
 for (const row of doc.querySelectorAll('.fields .row')) {
  const label=row.querySelector('.label')?.textContent || '';
  if (/^Source directory|^include$|^exclude$/.test(label)) assert.equal(row.querySelector('.copy').classList.contains('collapsed'),false);
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
{
 const dom = new JSDOM(pages.platform_control_review, {runScripts:'dangerously',pretendToBeVisual:true,
  beforeParse(w) {w.ResizeObserver=class {observe(){} disconnect(){}};}});
 const w=dom.window,doc=w.document,replies=[];
 w.postMessage=value=>replies.push(value);
 const form={action:'apply',resourceType:'agent',resourceKey:'candidate',before:'',after:'key: candidate\nmode: KBASE\nkbaseConfig:\n  libraryId: shared-library\n',
  knowledgeBinding:{libraryId:'shared-library',baseRevision:'library-revision',
   sourcePermissions:[{name:'documents',sourcePath:'/host/canonical-documents',include:['**/*.md'],exclude:['private/**']}]}};
 const send=(type,data)=>w.dispatchEvent(new w.MessageEvent('message',{source:w,data:{type,data}}));
 send('awaiting_init',{mode:'form',runId:'run',awaitingId:'wait',locale:'en-US',form:{data:form}});
 const text=doc.querySelector('.fields').textContent+doc.querySelector('.notes').textContent;
 for(const expected of ['shared-library','library-revision','/host/canonical-documents','**/*.md','private/**']) assert.ok(text.includes(expected),expected);
 for(const row of doc.querySelectorAll('.fields .row')){
  const label=row.querySelector('.label')?.textContent||'';
  if(/Bound knowledge library|Library configuration revision|Source directory|^include$|^exclude$/.test(label)) assert.equal(row.querySelector('.copy').classList.contains('collapsed'),false);
 }
 send('awaiting_collect',{runId:'run',awaitingId:'wait',decision:'submit'});
 assert.deepEqual(JSON.parse(JSON.stringify(replies.at(-1).param.data)),form);
 dom.window.close();
}
console.log('Library management and Agent binding reviews show canonical retrieval scope, safe text and immutable approval data.');
