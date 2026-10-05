const Review = (() => {
  'use strict';
  const own=(o,k)=>Object.prototype.hasOwnProperty.call(o||{},k);
  const record=v=>v&&typeof v==='object'&&!Array.isArray(v)?v:{};
  const node=(tag,cls,text)=>{const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=String(text);return n;};
  function mount(render) {
    let state=null;
    addEventListener('message',event=>{
      if(event.source!==parent)return;
      const message=event.data;
      if(!message||typeof message!=='object')return;
      if((message.type==='awaiting_init'||message.type==='awaiting_update')&&message.data?.mode==='form') {
        const data=message.data, form=record(data.form), args=record(form.args), current=record(form.current);
        const zh=String(data.locale||navigator.language).startsWith('zh'), t=(cn,en)=>zh?cn:en;
        document.documentElement.lang=zh?'zh-CN':'en';
        if(data.colorScheme==='dark'||data.colorScheme==='light')document.documentElement.dataset.theme=data.colorScheme;
        else delete document.documentElement.dataset.theme;
        const main=node('main'), header=node('header'), fields=node('section','fields'), notes=node('section');
        const details=node('details');details.id='technical-details';details.open=document.getElementById('technical-details')?.open||false;
        details.append(node('summary','',t('技术详情（可选）','Technical details (optional)')),node('pre','',JSON.stringify(form,null,2)));
        main.append(header,fields,notes,details,node('p','footer',t('请在下方选择同意或拒绝。需要修改时，可在拒绝时说明。','Use the host controls below to approve or reject. Add feedback if changes are needed.')));
        const format=v=>v===undefined?t('暂不可用','Unavailable'):v===null||v===''?t('未设置','Not set'):typeof v==='boolean'?(v?t('开启','On'):t('关闭','Off')):Array.isArray(v)?(v.length?v.map(format).join('、'):t('无','None')):typeof v==='object'?t('复合设置，请查看技术详情','Structured setting; see technical details'):String(v);
        const ui={t,own,record,args,current,form,action:form.action,format,
          title(category,title,intro){header.append(node('div','category',category),node('h1','',title),node('p','intro',intro));},
          target(name,subtitle,id){const box=node('div','target');box.append(node('strong','',name));if(subtitle)box.append(node('p','',subtitle));if(id&&!name.includes(id))box.append(node('p','',t('标识：','ID: ')+id));header.append(box);},
          field(label,value){const row=node('div','row');row.append(node('div','label',label),node('div','value',format(value)));fields.append(row);},
          change(label,key,value,formatter=format){
            if(!own(current,key)){this.field(label,formatter(value));return;}
            const row=node('div','row'), compare=node('div','compare'), old=node('div','old'), next=node('div','new');
            old.append(node('span','caption',t('当前','Current')),node('span','',formatter(current[key])));
            next.append(node('span','caption',t('改为','Change to')),node('span','',formatter(value)));
            compare.append(old,node('div','arrow','→'),next);row.append(node('div','label',label),compare);fields.append(row);
          },
          notice(text,warning=false){notes.append(node('p','notice'+(warning?' warning':''),text));},
          snapshot(expected=true){if(!expected)return;notes.append(node('p','snapshot',Object.keys(current).length?t('当前值来自刚读取的桌面状态；执行前仍会检查实际状态。','Current values were read from Desktop; the actual state is checked again at execution.'):t('暂未读取到当前设置。本页仅展示本次请求的修改内容。','Current settings could not be read. This review shows only the requested changes.')));},
          name(id,fallback){return record(form.names)[id]||id||fallback||t('未指定','Not specified');},
          extras(input,known){const extra=Object.keys(record(input)).filter(k=>!known.includes(k));if(extra.length)this.notice(t('还包含 '+extra.length+' 项附加设置，请展开技术详情核对。','There are '+extra.length+' additional settings. Expand technical details to review them.'),true);},
          required(value,label){if(value===undefined||value===null||value==='')this.notice(t('尚未提供'+label+'，需要补充后才能执行。',label+' is missing and must be supplied before execution.'),true);}
        };
        try {render(ui);state=data;} catch {state=null;main.replaceChildren(node('p','notice warning',t('无法显示这次操作，请拒绝并要求重新提交。','This operation could not be displayed. Reject it and request a new submission.')));}
        document.getElementById('review').replaceChildren(main);
      }
      if(message.type==='awaiting_collect'&&state&&message.data?.runId===state.runId&&message.data?.awaitingId===state.awaitingId&&message.data?.decision==='submit') {
        parent.postMessage({type:'frontend_awaiting_submit',params:[{id:state.activeFormId,decision:'approve',form:state.form||{}}]},'*');
      }
    });
  }
  return {mount};
})();
