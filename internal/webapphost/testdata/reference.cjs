const http=require('node:http'),fs=require('node:fs'),net=require('node:net'),crypto=require('node:crypto');
const server=http.createServer(async(req,res)=>{
 if(req.url==='/health'){res.end('ok');return}
 if(req.url==='/api/sse'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write('data: ready\n\n');const timer=setInterval(()=>res.write('data: tick\n\n'),50);req.on('close',()=>clearInterval(timer));return}
 if(req.url==='/api/crash'){res.end('crashing');setTimeout(()=>process.exit(12),20);return}
 if(req.url==='/api/echo'){res.setHeader('Set-Cookie',['ap_webapp_0123456789abcdef=forged; Path=/','business=ok; Path=/','bad=domain; Domain=127.0.0.1; Path=/']);res.end(JSON.stringify({cookie:req.headers.cookie||'',authorization:req.headers.authorization||'',forwarded:req.headers['x-forwarded-host']||'',identity:req.headers['x-agent-owner']||''}));return}
 if(req.url==='/api/probes'){
  const denied=(fn)=>{try{fn();return false}catch(error){return ['ERR_ACCESS_DENIED','EACCES','EPERM','ENOENT'].includes(error.code)}};
  fs.writeFileSync(process.env.WEBAPP_DATA_DIR+'/value','persistent');fs.writeFileSync(process.env.TMPDIR+'/temporary','temporary');
  const result={data:fs.readFileSync(process.env.WEBAPP_DATA_DIR+'/value','utf8'),config:JSON.parse(fs.readFileSync(process.env.WEBAPP_USER_CONFIG_FILE,'utf8')),policyDenied:denied(()=>fs.readFileSync(process.env.WEBAPP_DIR+'/../policy.json')),otherDenied:denied(()=>fs.readFileSync('/tmp/host-only-secret')),procDenied:denied(()=>fs.readFileSync('/proc/1/environ')),codeWriteDenied:denied(()=>fs.writeFileSync(__filename,'bad')),configWriteDenied:denied(()=>fs.writeFileSync(process.env.WEBAPP_USER_CONFIG_FILE,'{}')),spawnDenied:denied(()=>require('node:child_process').spawnSync('/bin/sh',['-c','true'])),platformSecretAbsent:!process.env.AP_ID_TOKEN&&!process.env.OPENAI_API_KEY};
  result.bindDenied=await new Promise(resolve=>{const s=net.createServer();s.once('error',e=>resolve(e.code==='EACCES'||e.code==='EPERM'));s.listen(0,'127.0.0.1',()=>{s.close();resolve(false)})});
  result.connectDenied=await new Promise(resolve=>{const c=net.connect(1,'127.0.0.1');c.once('error',e=>resolve(e.code==='EACCES'||e.code==='EPERM'));c.once('connect',()=>{c.destroy();resolve(false)})});
  console.log('probes-complete');res.end(JSON.stringify(result));return
 }
 res.statusCode=404;res.end('backend missing');
});
server.on('upgrade',(req,socket)=>{if(req.url!='/api/ws'){socket.destroy();return}const key=crypto.createHash('sha1').update(req.headers['sec-websocket-key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+key+'\r\n\r\n');socket.write(Buffer.from([0x81,5,...Buffer.from('ready')]));});
server.listen(Number(process.env.PORT),process.env.HOST,()=>console.log('reference-ready'));
