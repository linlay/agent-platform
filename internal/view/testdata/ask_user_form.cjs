const fs = require('node:fs');
const assert = require('node:assert/strict');
const {JSDOM} = require(process.argv[2]);
const {html:page,cases} = JSON.parse(fs.readFileSync(process.argv[3],'utf8'));
const dom = new JSDOM(page,{runScripts:'dangerously',beforeParse(w){w.TextEncoder=TextEncoder;}});
const w = dom.window, doc=w.document, replies=[];
w.postMessage=(value)=>replies.push(value);
const send=(type,data,source=w)=>w.dispatchEvent(new w.MessageEvent('message',{source,data:{type,data}}));
const fragment=`<label>Name<input name="name" required></label><input name="single" type="checkbox"><input name="group" type="checkbox" value="a"><input name="group" type="checkbox" value="b"><input name="r" type="radio" value="a"><input name="r" type="radio" value="b"><select name="many" multiple><option value="a">A</option><option value="b">B</option></select><textarea name="note"></textarea><input name="reportValidity" value="clobber"><input name="__proto__" value="safe"><fieldset disabled><input name="disabled" value="omit"></fieldset>`;
const init={mode:'form',runId:'run',awaitingId:'wait',locale:'en-US',colorScheme:'light',form:{title:'Profile',data:{html:fragment,values:{name:'Alice',single:'true',group:['b'],many:['a'],note:'hello'}}}};
const collect=decision=>send('awaiting_collect',{runId:'run',awaitingId:'wait',decision});
send('awaiting_init',init);
assert.equal(doc.querySelector('#heading').textContent,'Profile');
assert.equal(doc.querySelector('[name=name]').value,'Alice');
collect('submit');
assert.equal(replies.at(-1).type,'frontend_awaiting_submit');
assert.deepEqual(JSON.parse(JSON.stringify(replies.at(-1).param.data)),{name:'Alice',single:'true',group:['b'],many:['a'],note:'hello',reportValidity:'clobber',['__proto__']:'safe'});
const input=doc.querySelector('[name=name]');
input.value='Edited';
send('awaiting_update',{...init,colorScheme:'dark',locale:'zh-CN'});
assert.equal(input.value,'Edited');assert.equal(doc.querySelector('[name=name]'),input);
assert.equal(doc.documentElement.dataset.theme,'dark');
input.value='';collect('submit');
assert.equal(replies.at(-1).type,'frontend_awaiting_invalid');
const count=replies.length;
send('awaiting_collect',{runId:'other',awaitingId:'wait',decision:'submit'});assert.equal(replies.length,count);
collect('reject');assert.equal(replies.at(-1).param.decision,'reject');assert.equal(replies.at(-1).param.data.name,'');
const submitEvent=new w.Event('submit',{cancelable:true});doc.querySelector('form').dispatchEvent(submitEvent);assert.ok(submitEvent.defaultPrevented);
for (const html of ['<input name="n" onfocus="alert(1)">','<img src="https://example.test"><input name="n">','<input name="n" type="password">','<input name="n" style="position:fixed">']) {
 send('awaiting_init',{...init,form:{title:'bad',data:{html}}});
 assert.equal(doc.querySelector('form').children.length,0);
 collect('submit');assert.equal(replies.at(-1).type,'frontend_awaiting_invalid');
 collect('reject');assert.equal(replies.at(-1).param.decision,'reject');
}
for (const {name,html,valid} of cases) {
 send('awaiting_init',{...init,form:{title:name,data:{html}}});
 assert.equal(doc.querySelector('form').children.length > 0, valid, name);
}
send('awaiting_init',{...init,form:{title:'large',data:{html:'<textarea name="days"></textarea>'}}});
const textarea=doc.querySelector('textarea');
textarea.value='<'.repeat(65536-11);collect('submit');assert.equal(replies.at(-1).type,'frontend_awaiting_submit');
for (const extra of ['x','中','\u2028']) {
 textarea.value='<'.repeat(65536-11)+extra;collect('submit');
 assert.equal(replies.at(-1).type,'frontend_awaiting_invalid');
 assert.equal(textarea.value.length,65536-10);
 assert.match(doc.querySelector('#error').textContent,/64 KiB/);
}
collect('reject');assert.equal(replies.at(-1).param.decision,'reject');assert.equal(replies.at(-1).param.data,undefined);
textarea.value='shortened';collect('submit');assert.equal(replies.at(-1).param.data.days,'shortened');
dom.window.close();
console.log('Form shell: prefill, serialization, validation, decline, identity, theme, input preservation and unsafe HTML checks passed.');
