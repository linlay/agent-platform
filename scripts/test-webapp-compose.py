#!/usr/bin/env python3
"""Destructive acceptance checks, restricted to the isolated candidate project.

Run on the Docker host after preparing the documented disposable Compose
fixture. Never point this at a production instance. Only stdlib is required.
"""
import argparse
import base64
import http.cookiejar
import io
import json
import pathlib
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--fixture', type=pathlib.Path, required=True)
parser.add_argument('--repo', type=pathlib.Path, required=True)
args = parser.parse_args()
fixture = args.fixture.resolve()
compose = ['docker', 'compose', '-f', str(fixture/'compose.yml'), '-f', str(fixture/'compose.webapps.yml')]
def command(*argv):
    return subprocess.check_output(argv, cwd=fixture, text=True).strip()
cid = command(*compose, 'ps', '-q', 'agent-platform')
info = json.loads(command('docker', 'inspect', cid))[0]
assert info['Config']['Labels']['com.docker.compose.project'] == 'agent-platform-webapp-acceptance'
assert info['Config']['Image'] == 'agent-platform:webapp-candidate'
assert info['HostConfig']['ReadonlyRootfs'] and info['Config']['User'] == '10001:10001'
base = 'http://127.0.0.1:11973'
def b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b'=')
header = b64(b'{"alg":"RS256","typ":"JWT"}')
payload = b64(json.dumps({'sub':'webapp-candidate-owner','iss':'webapp-acceptance','aud':'webapp-acceptance','iat':int(time.time()),'exp':int(time.time())+3600}).encode())
unsigned = header+b'.'+payload
signature = subprocess.check_output(['openssl','dgst','-sha256','-sign',str(fixture/'test-key.pem')], input=unsigned)
token = (unsigned+b'.'+b64(signature)).decode()
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*unused):
        return None
jar = http.cookiejar.CookieJar()
browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect())
def request(url, data=None, method=None, headers=None, client=None):
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    try:
        response = (client or urllib.request.build_opener(NoRedirect())).open(req,timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    return response.status, response.headers, response.read()
def api(path, value=None, method=None, raw=None):
    data = raw if raw is not None else None if value is None else json.dumps(value).encode()
    status,_,body = request(base+'/api/admin/webapps'+path,data,method,{'Authorization':'Bearer '+token,'Origin':base,'Content-Type':'application/zip' if raw is not None else 'application/json'})
    result = json.loads(body)
    assert status in (200,202), (status,result)
    return result['data']
def wait_operation(op):
    deadline = time.monotonic()+40
    while time.monotonic()<deadline:
        result = api('/operations/'+op['operationId'])
        if result['state'] in ('succeeded','failed'):
            return result
        time.sleep(.1)
    raise AssertionError('operation timed out')
app_id = 'webapp-c011ec7ab1e00001'
def package(version, broken=False):
    manifest = {'schemaVersion':2,'id':app_id,'key':'compose-reference','label':'Compose reference','version':version,'target':'linux-x64','appConfig':{'version':version},'frontend':{'root':'frontend','index':'index.html','routeConfig':{'backendPrefixes':['/api']}},'backend':{'command':{'type':'electron-node','entry':'backend/server.cjs'},'args':[],'env':{},'health':{'type':'http','path':'/health','startupTimeoutMs':3000},'shutdownTimeoutMs':1000}}
    source = """const http=require('node:http'),fs=require('node:fs');
const data=process.env.WEBAPP_DATA_DIR+'/count';let count=0;try{count=Number(fs.readFileSync(data,'utf8'))}catch{};fs.writeFileSync(data,String(++count));
const version=VERSION;
http.createServer(async(req,res)=>{if(req.url==='/health'){res.statusCode=BROKEN?503:200;res.end('health');return}
if(req.url==='/api/state'){res.end(JSON.stringify({count,version}));return}
if(req.url==='/api/sdk'){try{const {createBackendClient}=await import('./bridge.mjs');const client=createBackendClient({url:process.env.DESKTOP_ACTION_BRIDGE_URL,token:process.env.DESKTOP_ACTION_BRIDGE_TOKEN});res.end(JSON.stringify(await client.connector.list()))}catch(e){res.statusCode=500;res.end(e.code||'failed')}return}
if(req.url==='/api/oom'){res.end('allocating');setTimeout(()=>{const chunks=[];for(let i=0;i<32;i++)chunks.push(Buffer.alloc(16*1024*1024,1));global.retained=chunks},20);return}\nif(req.url==='/api/crash'){res.end('crashing');setTimeout(()=>process.exit(12),20);return}
res.statusCode=404;res.end('missing')}).listen(Number(process.env.PORT),process.env.HOST);
""".replace('VERSION',json.dumps(version)).replace('BROKEN','true' if broken else 'false')
    out=io.BytesIO()
    with zipfile.ZipFile(out,'w',zipfile.ZIP_DEFLATED) as archive:
        for name,data in {'webapp.json':json.dumps(manifest),'frontend/index.html':'<!doctype html><h1>Compose reference</h1>','backend/server.cjs':source,'backend/bridge.mjs':(args.repo/'internal/webappbridge/sdk/bridge.mjs').read_text()}.items():
            archive.writestr(app_id+'/'+name,data)
    return out.getvalue()
def install(version,broken=False):
    upload=api('/uploads',raw=package(version,broken))
    review=api('/preflight',{'uploadId':upload['uploadId']})
    return wait_operation(api('/install',{'uploadId':upload['uploadId'],'sha256':upload['sha256'],'expectedRevision':review['expectedRevision']}))
def entry():
    return api('/'+app_id)['entry']
def state(action):
    current=entry()
    op=wait_operation(api('/'+app_id+'/'+action,{'expectedRevision':current['revision'],'expectedStateRevision':current['stateRevision']}))
    assert op['state']=='succeeded',op
    return entry()
def running(previous=None):
    deadline=time.monotonic()+30
    current = None
    while time.monotonic()<deadline:
        try:
            current=entry()
            if current['observedState']=='running' and current.get('generation')!=previous:
                return current
        except (OSError, AssertionError, json.JSONDecodeError):
            pass
        time.sleep(.2)
    raise AssertionError(('not running',current))
def launch():
    ticket=api('/'+app_id+'/launch-ticket',{'returnPath':'/'})
    status,_,_=request(ticket['action'],urllib.parse.urlencode({'ticket':ticket['ticket']}).encode(),'POST',{'Origin':base,'Content-Type':'application/x-www-form-urlencoded'},browser)
    assert status==303,status
    assert request(ticket['action'],urllib.parse.urlencode({'ticket':ticket['ticket']}).encode(),'POST',{'Origin':base,'Content-Type':'application/x-www-form-urlencoded'},browser)[0]==403
    return ticket['action'].split('/__desktop/')[0]
def app_json(origin,path):
    status,_,body=request(origin+path,headers={'Origin':origin},client=browser)
    assert status==200,(status,body)
    return json.loads(body)
def report(name):
    print('PASS '+name,flush=True)

assert not api('')['items'], 'fixture must be empty'
assert install('1.0.0')['state']=='succeeded'
state('start');first=running();origin=launch()
assert app_json(origin,'/api/state')['count']==1
assert app_json(origin,'/api/sdk')['items']==[]
status,_,_=request(origin+'/__desktop/actions/call',b'{"action":"desktop.capabilities.list","args":{}}','POST',{'Content-Type':'application/json','Origin':origin},browser)
assert status==403
csrf=app_json(origin,'/__desktop/bootstrap.json')['csrfToken']
status,_,body=request(origin+'/__desktop/actions/call',b'{"action":"desktop.capabilities.list","args":{}}','POST',{'Content-Type':'application/json','Origin':origin,'X-WebApp-CSRF':csrf},browser)
assert status==200 and json.loads(body)['ok']
assert request(base+'/api/admin/webapps',headers={'Authorization':'Bearer '+token,'Origin':origin})[0]==403
report('ZIP / local launch one-use / CSRF / main Origin / backend SDK')
assert install('1.1.0')['state']=='succeeded'
updated=running(first['generation']);origin=launch()
assert app_json(origin,'/api/state')['version']=='1.1.0'
assert install('1.2.0',True)['state']=='failed'
rolled=running(updated['generation']);origin=launch()
assert app_json(origin,'/api/state')['version']=='1.1.0'
report('running update and unhealthy update rollback')

# Kill only the candidate Platform process, allowing the container init and
# supervisor death cleanup to run. Compose restart policy restores desired.
command('docker','exec',cid,'sh','-c','kill -KILL $(pidof agent-platform)')
time.sleep(2)
restored=running(rolled['generation']);origin=launch()
assert app_json(origin,'/api/sdk')['items']==[]
assert app_json(origin,'/api/state')['count']>=4
report('Platform SIGKILL / new generation / durable data / refreshed SDK grant')
command(*compose,'restart','agent-platform')
restored2=running(restored['generation']);origin=launch()
assert app_json(origin,'/api/sdk')['items']==[]
report('Compose restart / desired state / no stale generation adoption')
state('stop');state('start');running(restored2['generation']);origin=launch()
assert app_json(origin,'/api/state')['version']=='1.1.0'
report('explicit stop and restart preserves data')
# Reduce only this disposable candidate's cgroup, then allocate from its
# supervised Node. Restore the limit even if the assertion fails.
assert install('1.3.0')['state']=='succeeded'
previous=running()['generation'];origin=launch()
since=int(time.time())-1
try:
    command('docker','update','--memory','128m','--memory-swap','128m',cid)
    request(origin+'/api/oom',headers={'Origin':origin},client=browser)
    time.sleep(3)
finally:
    command('docker','update','--memory','1g','--memory-swap','2g',cid)
oom=command('docker','events','--since',str(since),'--until',str(int(time.time())), '--filter','container='+cid,'--filter','event=oom','--format','{{.Action}}')
assert 'oom' in oom, 'candidate did not trigger cgroup OOM'
running(previous);origin=launch()
assert app_json(origin,'/api/sdk')['items']==[]
report('supervised Node cgroup OOM / restored resource limit / recovery')
# Leave a running, authenticated candidate for the browser acceptance step.
print(json.dumps({'appId':app_id,'origin':origin,'image':info['Image']}),flush=True)
