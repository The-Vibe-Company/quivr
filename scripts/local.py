#!/usr/bin/env python3
"""Linux local text slice: host Go processes and isolated real dependencies."""
from prepare_tokenizer import prepare as prepare_tokenizer
from prepare_embeddings import prepare as prepare_embeddings, MODEL
import argparse, json, os, pathlib, secrets, signal, subprocess, time, urllib.request, uuid
ROOT=pathlib.Path(__file__).resolve().parents[1]
GO=os.environ.get('GO','go')

def run(args, **kwargs):
    return subprocess.run(args, check=True, cwd=ROOT, **kwargs)

def port():
    import socket
    with socket.socket() as s:
        s.bind(('127.0.0.1',0)); return s.getsockname()[1]

class Stack:
    def __init__(self, name):
        self.name=name
        self.directory=ROOT/'.scratch'/name
        self.directory.mkdir(parents=True,exist_ok=True,mode=0o700)
        self.directory.chmod(0o700)
        self.statefile=self.directory/'state.json'
        if self.statefile.exists(): self.state=json.loads(self.statefile.read_text())
        else:
            self.state={'password':secrets.token_hex(24),'cursor_key':secrets.token_hex(32), 'admin':secrets.token_hex(32),'other':secrets.token_hex(32),'reader':secrets.token_hex(32),'scoped':secrets.token_hex(32),'denied':secrets.token_hex(32),'pids':[], 'api_port':port(),'probe_port':port(),'worker_probe_port':port()}
            self.save()
        for key in ['s3_access','s3_secret','writer']:
            self.state.setdefault(key,secrets.token_hex(24))
        self.save()
        identities={'identities':[{'name':'local-core','credentials':[{'accessKey':self.state['s3_access'],'secretKey':self.state['s3_secret']}],'actions':['Admin','Read','Write','List','Tagging']}]}
        # The read-only bind mount must be readable by Seaweed's container UID.
        # The enclosing 0700 directory keeps these credentials private on the host.
        s3file=self.directory/'s3.json';s3file.write_text(json.dumps(identities));s3file.chmod(0o644)
    def save(self):
        self.statefile.write_text(json.dumps(self.state));self.statefile.chmod(0o600)
    def compose(self,*args,**kwargs):
        return run(['docker','compose','-p',self.name,'-f','deploy/compose/compose.yaml',*args],env={**os.environ,'QUIVR_DB_PASSWORD':self.state['password'],'QUIVR_LOCAL_ROOT':str(self.directory),'QUIVR_MODEL_ROOT':str(MODEL)},**kwargs)
    def config(self):
        address=self.compose('port','postgres','5432',capture_output=True,text=True).stdout.strip()
        s=self.state
        weaviate=self.compose('port','weaviate','8080',capture_output=True,text=True).stdout.strip()
        temporal=self.compose('port','temporal','7233',capture_output=True,text=True).stdout.strip()
        seaweed=self.compose('port','seaweed','8333',capture_output=True,text=True).stdout.strip()
        scope=lambda org,actions,corpora:dict(organization=org,actions=actions,corpora=corpora)
        tei_container=self.compose('ps','-q','tei',capture_output=True,text=True).stdout.strip()
        tei=run(['docker','inspect',tei_container,'--format','{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'],capture_output=True,text=True).stdout.strip()+':80'
        cfg=dict(tei_url='http://'+tei,tokenizer=prepare_tokenizer(),weaviate_url='http://'+weaviate,temporal_address=temporal,s3=dict(endpoint='http://'+seaweed,access_key=s['s3_access'],secret_key=s['s3_secret'],bucket='quivr-content'),log_directory=str(self.directory),database_url=f"postgres://quivr:{s['password']}@{address}/quivr?sslmode=disable",listen=f"127.0.0.1:{s['api_port']}",probe_listen=f"127.0.0.1:{s['probe_port']}",cursor_key=s['cursor_key'],keys={
            s['admin']:scope('org_a',['corpora:read','corpora:write','content:read','content:write','search:query'],['*']),
            s['other']:scope('org_b',['corpora:read','corpora:write','content:read','content:write','search:query'],['*']),
            s['reader']:scope('org_a',['corpora:read'],['*']),
            s['scoped']:scope('org_a',['corpora:read','corpora:write','content:read','content:write','search:query'],[s.get('scoped_id','corpus_not_granted')]),
            s['writer']:scope('org_a',['content:write'],['*']),
            s['denied']:scope('org_a',['content:read'],['*'])})
        f=self.directory/'config.json';f.write_text(json.dumps(cfg));f.chmod(0o600)
        (self.directory/'tokenizer-provenance.json').write_text((ROOT/'internal/processing/profile.json').read_text())
        worker=self.directory/'worker.json';cfg['probe_listen']=f"127.0.0.1:{s['worker_probe_port']}";worker.write_text(json.dumps(cfg));worker.chmod(0o600)
    def migrate(self):
        self.config()
        with (self.directory/'migrate-startup.log').open('w') as log:
            run([str(self.directory/'quivr'),'migrate'],env={**os.environ,'QUIVR_CONFIG':str(self.directory/'config.json')},stdout=log,stderr=log)
    def start_processes(self):
        for command,config in [('api','config.json'),('worker','worker.json')]:
            with (self.directory/(command+'-startup.log')).open('w') as log:
                p=subprocess.Popen([str(self.directory/'quivr'),command],cwd=ROOT,env={**os.environ,'QUIVR_CONFIG':str(self.directory/config)},stdout=log,stderr=log,start_new_session=True)
            self.state['pids'].append(p.pid);self.save()
        for key in ['probe_port','worker_probe_port']:
            deadline=time.monotonic()+20
            while True:
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{self.state[key]}/readyz",timeout=1) as r:
                        if r.status==204:break
                except OSError:pass
                if time.monotonic()>deadline:raise RuntimeError('readiness timeout; inspect scoped logs')
                time.sleep(.1)
    def stop_processes(self):
        for pid in self.state['pids']:
            try:
                # Refuse to signal a reused PID belonging to any unrelated program.
                cmd=pathlib.Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')[0]
                if cmd==str(self.directory/'quivr').encode():os.kill(pid,signal.SIGTERM)
            except (FileNotFoundError,ProcessLookupError):pass
        self.state['pids']=[];self.save()
        time.sleep(.15)
    def up(self):
        self.stop_processes()
        prepare_tokenizer()
        (self.directory/'embedding-provenance.json').write_text(json.dumps(prepare_embeddings(),indent=2))
        run([GO,'build','-o',str(self.directory/'quivr'),'./cmd/quivr'])
        self.compose('up','-d','--wait','--wait-timeout','180')
        self.migrate();self.migrate();self.start_processes()
    def tests(self,pattern):
        s=self.state
        env={**os.environ,'QUIVR_TEST_CAPTURES':str(self.directory),'QUIVR_TEST_URL':f"http://127.0.0.1:{s['api_port']}",**{'QUIVR_TEST_'+k.upper():s[k] for k in ['admin','other','reader','scoped','denied','writer']}}
        with (self.directory/'acceptance.log').open('a') as log:
            result=subprocess.run([GO,'test','-count=1','-v','-run',pattern,'./tests/acceptance'],cwd=ROOT,env=env,stdout=log,stderr=subprocess.STDOUT)
        if result.returncode:raise RuntimeError('acceptance failed; inspect '+str(self.directory/'acceptance.log'))
    def ingestion_outages(self):
        s=self.state
        base=f"http://127.0.0.1:{s['api_port']}"
        def call(method,path,body=None,expected=None):
            data=json.dumps(body).encode() if body is not None else None
            req=urllib.request.Request(base+path,data=data,method=method,headers={'Authorization':'Bearer '+s['admin'],'Content-Type':'application/json'})
            try: response=urllib.request.urlopen(req,timeout=8)
            except urllib.error.HTTPError as error: response=error
            with response:
                assert response.status==(expected or (202 if path=='/v0/records' else 201 if method=='POST' and path=='/v0/corpora' else 200)),response.status
                result=json.load(response)
                capture=dict(path=path,method=method,status=response.status,body=result)
                (self.directory/('response-'+uuid.uuid4().hex+'.json')).write_text(json.dumps(capture))
                return result
        corpus=call('POST','/v0/corpora',{'name':'Fault recovery','idempotency_key':'fault-corpus'})['corpus_id']
        for dependency in ['temporal','seaweed']:
            self.compose('stop',dependency)
            command={'idempotency_key':'outage-'+dependency,'source':{'corpus_id':corpus,'namespace':'faults','record_key':dependency},'content':{'kind':'text','text':'Durable '+dependency+' input'}}
            accepted=call('POST','/v0/records',command);rid=accepted['receipt_id']
            assert accepted['state']=='pending' and 'outcome' not in accepted
            time.sleep(1)
            pending=call('GET','/v0/ingestion-receipts/'+rid)
            assert pending['state']=='pending' and 'outcome' not in pending
            # Kill all application processes while durable work is pending.
            for pid in s['pids']:
                try: os.kill(pid,signal.SIGKILL)
                except ProcessLookupError: pass
            s['pids']=[];self.save()
            self.compose('start',dependency)
            self.config()
            self.start_processes()
            replay=call('POST','/v0/records',command);assert replay['receipt_id']==rid
            deadline=time.monotonic()+45
            while True:
                receipt=call('GET','/v0/ingestion-receipts/'+rid)
                if receipt['state']=='resolved':break
                assert time.monotonic()<deadline,receipt
                time.sleep(.2)
            assert receipt['outcome']=='created',receipt
            version=call('GET','/v0/records/'+receipt['record_id']+'/versions/'+receipt['version_id'])
            assert version['manifest']['parts'][0]['content']['text']=='Durable '+dependency+' input'
        def await_ready(rid):
            deadline=time.monotonic()+45
            while True:
                receipt=call('GET','/v0/ingestion-receipts/'+rid)
                if receipt.get('availability',{}).get('searchable'): return receipt
                assert time.monotonic()<deadline,receipt
                time.sleep(.2)
        command={'idempotency_key':'search-before-outage','source':{'corpus_id':corpus,'namespace':'faults','record_key':'search'},'content':{'kind':'text','text':'Ancienne comète'}}
        first=await_ready(call('POST','/v0/records',command)['receipt_id'])
        self.compose('stop','weaviate')
        command['idempotency_key']='search-during-outage';command['content']['text']='Nouvelle galaxie 🌌'
        rid=call('POST','/v0/records',command)['receipt_id']
        deadline=time.monotonic()+30
        while True:
            receipt=call('GET','/v0/ingestion-receipts/'+rid)
            if receipt['state']=='resolved' and receipt['processing']['state']=='retrying':break
            assert time.monotonic()<deadline,receipt
            time.sleep(.2)
        assert receipt['outcome']=='created' and receipt['diagnostics'],receipt
        assert not receipt['availability']['is_current'] and not receipt['availability']['searchable'],receipt
        record=call('GET','/v0/records/'+receipt['record_id'])
        assert record['current_version_id']==first['version_id'],record
        version=call('GET','/v0/records/'+receipt['record_id']+'/versions/'+receipt['version_id'])
        assert version['manifest']['parts'][0]['content']['text']=='Nouvelle galaxie 🌌'
        query={'query':'galaxie','corpus_ids':[corpus],'mode':'lexical'}
        error=call('POST','/v0/search',query,expected=503)
        assert error['retryable'] and error['code']=='search_unavailable',error
        for pid in s['pids']:
            try:os.kill(pid,signal.SIGKILL)
            except ProcessLookupError:pass
        s['pids']=[];self.save();self.compose('start','weaviate');self.config();self.start_processes()
        assert call('POST','/v0/records',command)['receipt_id']==rid
        ready=await_ready(rid);assert ready['version_id']==receipt['version_id'],ready
        results=call('POST','/v0/search',query)['items']
        assert len(results)==1 and results[0]['excerpt']['text']=='Nouvelle galaxie 🌌',results
        query['query']='comète'
        assert call('POST','/v0/search',query)['items']==[] # Old projection remains, canonical hydration suppresses it.
        (self.directory/'outages.json').write_text(json.dumps({'temporal':'passed','seaweed':'passed','weaviate':'passed','worker_kill_and_replay':'passed','delayed_promotion_and_stale_candidate':'passed'}))
    def capture(self):
        for service in ['postgres','temporal','seaweed','weaviate','tei']:
            with (self.directory/(service+'.log')).open('w') as log:self.compose('logs','--no-color',service,stdout=log,stderr=log)
    def down(self,reset=False):
        self.stop_processes();self.compose('down',*(['--volumes'] if reset else []))

def main():
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['dev','verify','down','reset','migrate']);args=parser.parse_args()
    verification=args.command=='verify'
    stack=Stack('quivr-verify-'+uuid.uuid4().hex[:10] if verification else 'quivr-dev-'+__import__('hashlib').sha256(str(ROOT).encode()).hexdigest()[:10])
    def interrupted(*_):raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM,interrupted)
    start=time.monotonic();status='failed'
    try:
        if args.command in ['dev','verify']:
            stack.up()
            if verification:
                stack.tests('TestCorpusPersistsAndReplays')
                req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/corpora",headers={'Authorization':'Bearer '+stack.state['admin']})
                with urllib.request.urlopen(req,timeout=5) as r: original_id=json.load(r)['items'][0]['corpus_id']
                (stack.directory/'original-corpus-id.txt').write_text(original_id)
                # Restart all application processes and PostgreSQL; assert via HTTP again.
                stack.stop_processes();stack.compose('restart','postgres');stack.compose('up','-d','--wait','--wait-timeout','180');stack.migrate();stack.start_processes()
                req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/corpora/{original_id}",headers={'Authorization':'Bearer '+stack.state['admin']})
                with urllib.request.urlopen(req,timeout=5) as r: assert json.load(r)['corpus_id']==original_id
                stack.tests('TestCorpusPersistsAndReplays')
                req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/corpora",headers={'Authorization':'Bearer '+stack.state['admin']})
                with urllib.request.urlopen(req,timeout=5) as r:stack.state['scoped_id']=json.load(r)['items'][0]['corpus_id']
                stack.save();stack.stop_processes();stack.config();stack.start_processes();stack.tests('TestAuthorization|TestValidation|TestPagination|TestConcurrent|TestInline|TestLexical|TestLong|TestSemantic')
                stack.stop_processes()
                with (stack.directory/'adapters.log').open('w') as log:
                    run([GO,'test','-count=1','-v','./internal/adapters/...','./internal/processing/...'],env={**os.environ,'QUIVR_ADAPTER_CONFIG':str(stack.directory/'config.json')},stdout=log,stderr=log)
                stack.start_processes()
                stack.ingestion_outages()
                from embedding_outage import verify as verify_embedding_outage
                verify_embedding_outage(stack)
                run([os.environ.get('CONTRACT_PYTHON',str(ROOT/'.scratch/contracts/venv/bin/python')),'scripts/validate_captures.py',str(stack.directory)])
            else:print(f"API http://127.0.0.1:{stack.state['api_port']} — credentials in {stack.directory}/config.json")
        elif args.command=='migrate':stack.migrate()
        else:stack.down(args.command=='reset')
        status='passed'
    finally:
        if verification:
            try:stack.capture()
            finally:stack.down(True)
            (stack.directory/'report.json').write_text(json.dumps({'status':status,'duration_seconds':round(time.monotonic()-start,3),'source':run(['git','rev-parse','HEAD'],capture_output=True,text=True).stdout.strip(),'scope':'Corpus, ingestion, lexical/semantic/hybrid HTTP acceptance, E5 enrichment/outage and FR/EN relevance; real PostgreSQL, Temporal, S3, Weaviate and TEI','artifacts':str(stack.directory)},indent=2))
            print('Verification artifacts:',stack.directory)
if __name__=='__main__':main()
