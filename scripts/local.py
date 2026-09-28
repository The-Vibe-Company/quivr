#!/usr/bin/env python3
"""Linux local text slice: host Go processes and isolated real dependencies."""
from prepare_tokenizer import prepare as prepare_tokenizer
from prepare_embeddings import prepare as prepare_embeddings, MODEL
import argparse, base64, json, os, pathlib, secrets, signal, subprocess, time, urllib.request, uuid
ROOT=pathlib.Path(__file__).resolve().parents[1]
GO=os.environ.get('GO','go')

def run(args, **kwargs):
    return subprocess.run(args, check=True, cwd=ROOT, **kwargs)

# Obvious local test signing secret of the capture receiver destination.
CAPTURE_DESTINATION='local-receiver-capture'
CAPTURE_SECRET='whsec_'+base64.b64encode(b'local-test-signing-secret-capture').decode()
# Shortened webhook retry policy of the local harness, like its other short intervals (dev and verify;
# deployment defaults: 1s/5m/24h/10s). Verification reports it in report.json.
DELIVERY_OVERRIDES={'retry_initial':'2s','retry_max':'5s','window':'60s'}
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
        for key,value in [('short_api_port',port()),('short_probe_port',port()),('receiver_port',port()),('graph_port',port()),('fake_x_port',port())]:
            self.state.setdefault(key,value)
        for key in ['s3_access','s3_secret','writer','connector','connector_scoped','credential_key','configurer']:
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
        cfg=dict(tei_url='http://'+tei,tokenizer=prepare_tokenizer(),weaviate_url='http://'+weaviate,temporal_address=temporal,s3=dict(endpoint='http://'+seaweed,access_key=s['s3_access'],secret_key=s['s3_secret'],bucket='quivr-content'),log_directory=str(self.directory),database_url=f"postgres://quivr:{s['password']}@{address}/quivr?sslmode=disable",listen=f"127.0.0.1:{s['api_port']}",probe_listen=f"127.0.0.1:{s['probe_port']}",cursor_key=s['cursor_key'],credential_key=s['credential_key'],connector_fixtures=True,connector_min_interval='1s',connector_rss_allow_private_addresses=True,
            # The m365_mail kind talks to the local fake Graph (scripts/fake_graph.py), never to Microsoft.
            x=dict(api_endpoint=f"http://127.0.0.1:{s['fake_x_port']}"),m365=dict(login_endpoint=f"http://127.0.0.1:{s['graph_port']}",graph_endpoint=f"http://127.0.0.1:{s['graph_port']}/v1.0"),keys={
            s['admin']:scope('org_a',['corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write'],['*']),
            s['other']:scope('org_b',['corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write','connectors:read','connectors:write'],['*']),
            # Connector acceptance owns org_c so its scheduled load cannot skew org_a/org_b scenarios.
            s['connector']:scope('org_c',['corpora:read','corpora:write','content:read','content:write','changes:read','connectors:read','connectors:write','blobs:read'],['*']),
            s['connector_scoped']:scope('org_c',['connectors:read','connectors:write'],['corpus_not_granted']),
            s['reader']:scope('org_a',['corpora:read'],['*']),
            s['scoped']:scope('org_a',['corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write'],[s.get('scoped_id','corpus_not_granted')]),
            s['writer']:scope('org_a',['content:write'],['*']),
            s['denied']:scope('org_a',['content:read'],['*']),
            # Corpus writer without operations:write: cannot change retrieval configuration.
            s['configurer']:scope('org_a',['corpora:read','corpora:write'],['*'])},
            # One deployment-configured webhook destination per Organization. These are obvious
            # local test values; real deployments reference the signing secret through secret_env.
            destinations={'local-receiver-org-a':dict(organization='org_a',url='http://127.0.0.1:9/local-receiver-org-a',secret='whsec_'+base64.b64encode(b'local-test-signing-secret-org-a!').decode()),
                          'local-receiver-org-b':dict(organization='org_b',url='http://127.0.0.1:9/local-receiver-org-b',secret='whsec_'+base64.b64encode(b'local-test-signing-secret-org-b!').decode()),
                          # Signed-delivery acceptance runs its own receiver on this port while it executes.
                          CAPTURE_DESTINATION:dict(organization='org_a',url=f"http://127.0.0.1:{s['receiver_port']}/capture",secret=CAPTURE_SECRET)},
            delivery=DELIVERY_OVERRIDES)
        f=self.directory/'config.json';f.write_text(json.dumps(cfg));f.chmod(0o600)
        (self.directory/'tokenizer-provenance.json').write_text((ROOT/'internal/processing/profile.json').read_text())
        # A second API over the same database with a short change retention proves public cursor expiry.
        short=self.directory/'short-retention.json';short.write_text(json.dumps({**cfg,'listen':f"127.0.0.1:{s['short_api_port']}",'probe_listen':f"127.0.0.1:{s['short_probe_port']}",'change_retention':'2s'}));short.chmod(0o600)
        worker=self.directory/'worker.json';cfg['probe_listen']=f"127.0.0.1:{s['worker_probe_port']}";worker.write_text(json.dumps(cfg));worker.chmod(0o600)
    def migrate(self):
        self.config()
        with (self.directory/'migrate-startup.log').open('w') as log:
            run([str(self.directory/'quivr'),'migrate'],env={**os.environ,'QUIVR_CONFIG':str(self.directory/'config.json')},stdout=log,stderr=log)
    def spawn(self,command,config):
        with (self.directory/(command+'-startup.log')).open('a') as log:
            p=subprocess.Popen([str(self.directory/'quivr'),command],cwd=ROOT,env={**os.environ,'QUIVR_CONFIG':str(self.directory/config)},stdout=log,stderr=log,start_new_session=True)
        self.state['pids'].append(p.pid)
        if command=='worker':self.state['worker_pid']=p.pid
        self.save()
    def await_ready(self,key):
        deadline=time.monotonic()+20
        while True:
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{self.state[key]}/readyz",timeout=1) as r:
                    if r.status==204:break
            except OSError:pass
            if time.monotonic()>deadline:raise RuntimeError('readiness timeout; inspect scoped logs')
            time.sleep(.1)
    def start_fake_graph(self):
        if getattr(self,'fake_graph',None) is None:
            from fake_graph import FakeGraph
            self.fake_graph=FakeGraph(self.state['graph_port'])
    def start_processes(self):
        self.start_fake_graph()
        for command,config in [('api','config.json'),('worker','worker.json')]:self.spawn(command,config)
        for key in ['probe_port','worker_probe_port']:self.await_ready(key)
    def signal_owned(self,pid,sig):
        try:
            # Refuse to signal a reused PID belonging to any unrelated program.
            cmd=pathlib.Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')[0]
            if cmd==str(self.directory/'quivr').encode():os.kill(pid,sig)
        except (FileNotFoundError,ProcessLookupError):pass
    def stop_worker(self):
        """Stop only the worker; the API keeps accepting durable commands."""
        pid=self.state.pop('worker_pid',None)
        if pid is None:raise RuntimeError('worker not tracked')
        self.signal_owned(pid,signal.SIGKILL)
        self.state['pids']=[p for p in self.state['pids'] if p!=pid];self.save()
        deadline=time.monotonic()+10
        while pathlib.Path(f'/proc/{pid}').exists() and time.monotonic()<deadline:time.sleep(.05)
    def start_worker(self):
        self.spawn('worker','worker.json');self.await_ready('worker_probe_port')
    def start_short_retention_api(self):
        with (self.directory/'short-api-startup.log').open('w') as log:
            p=subprocess.Popen([str(self.directory/'quivr'),'api'],cwd=ROOT,env={**os.environ,'QUIVR_CONFIG':str(self.directory/'short-retention.json')},stdout=log,stderr=log,start_new_session=True)
        self.state['pids'].append(p.pid);self.save()
        deadline=time.monotonic()+20
        while True:
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{self.state['short_probe_port']}/readyz",timeout=1) as r:
                    if r.status==204:break
            except OSError:pass
            if time.monotonic()>deadline:raise RuntimeError('short-retention API readiness timeout')
            time.sleep(.1)
    def stop_processes(self):
        for pid in self.state['pids']:self.signal_owned(pid,signal.SIGTERM)
        self.state['pids']=[];self.state.pop('worker_pid',None);self.save()
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
        env={**os.environ,'QUIVR_TEST_CAPTURES':str(self.directory),'QUIVR_TEST_URL':f"http://127.0.0.1:{s['api_port']}",**{'QUIVR_TEST_'+k.upper():s[k] for k in ['admin','other','reader','scoped','denied','writer','connector','connector_scoped','configurer']},'QUIVR_TEST_SHORT_RETENTION_URL':f"http://127.0.0.1:{s['short_api_port']}",'QUIVR_TEST_RECEIVER_ADDR':f"127.0.0.1:{s['receiver_port']}",'QUIVR_TEST_RECEIVER_SECRET':CAPTURE_SECRET,'QUIVR_TEST_WORKER_PROBE_URL':f"http://127.0.0.1:{s['worker_probe_port']}",'QUIVR_TEST_FAKE_GRAPH_URL':f"http://127.0.0.1:{s['graph_port']}",'QUIVR_TEST_FAKE_X_URL':f"http://127.0.0.1:{s['fake_x_port']}"}
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
                stack.save();stack.stop_processes();stack.config();stack.start_processes();stack.tests('TestAuthorization|TestValidation|TestPagination|TestConcurrent|TestInline|TestStructuredManifest|TestManifest|TestWithdrawal|TestCorrection|TestLexical|TestLong|TestSemantic|TestUpload|TestBatch')
                stack.stop_processes()
                with (stack.directory/'adapters.log').open('w') as log:
                    run([GO,'test','-count=1','-v','./internal/adapters/...','./internal/processing/...'],env={**os.environ,'QUIVR_ADAPTER_CONFIG':str(stack.directory/'config.json')},stdout=log,stderr=log)
                stack.start_processes()
                stack.ingestion_outages()
                from embedding_outage import verify as verify_embedding_outage
                verify_embedding_outage(stack)
                from rebuild_recovery import verify as verify_rebuild_recovery
                verify_rebuild_recovery(stack)
                from operation_control import verify as verify_operation_control
                verify_operation_control(stack)
                # Change-feed, catalog resync and rebuild tests add Corpora and ingestion load; run them last so they cannot skew
                # order-sensitive acceptance or timed outage scenarios.
                stack.start_short_retention_api();stack.tests('TestChange|TestCatalog|TestRebuild|TestRetrievalConfiguration')
                # Monitoring definitions use their own Corpora and light ingestion; run after timed scenarios.
                stack.tests('TestMonitoring')
                # A Delivery with one failed attempt converges after the worker is killed and restarted.
                stack.tests('TestDeliveryRestartBefore');stack.stop_worker();stack.start_worker();stack.tests('TestDeliveryRestartAfter')
                # Connector acquisition keeps polling on its schedule; run it after every
                # timed scenario, in its own Organization, then prove restart resumption.
                # The x_list kind polls a local fake X API served from this process.
                import fake_x
                fake_x.start(stack.state['fake_x_port'])
                stack.tests('TestConnector')
                from connector_restart import verify as verify_connector_restart
                verify_connector_restart(stack)
                from m365_restart import verify as verify_m365_restart
                verify_m365_restart(stack)
                from connector_x_restart import verify as verify_connector_x_restart
                verify_connector_x_restart(stack,f"http://127.0.0.1:{stack.state['fake_x_port']}")
                run([os.environ.get('CONTRACT_PYTHON',str(ROOT/'.scratch/contracts/venv/bin/python')),'scripts/validate_captures.py',str(stack.directory)])
            else:print(f"API http://127.0.0.1:{stack.state['api_port']} — credentials in {stack.directory}/config.json")
        elif args.command=='migrate':stack.migrate()
        else:stack.down(args.command=='reset')
        status='passed'
    finally:
        if verification:
            try:stack.capture()
            finally:stack.down(True)
            (stack.directory/'report.json').write_text(json.dumps({'status':status,'duration_seconds':round(time.monotonic()-start,3),'source':run(['git','rev-parse','HEAD'],capture_output=True,text=True).stdout.strip(),'scope':'Corpus, ingestion, lexical/semantic/hybrid HTTP acceptance, E5 enrichment/outage and FR/EN relevance; real PostgreSQL, Temporal, S3, Weaviate and TEI','timing_overrides':{'delivery':DELIVERY_OVERRIDES,'change_retention_short_api':'2s'},'artifacts':str(stack.directory)},indent=2))
            print('Verification artifacts:',stack.directory)
if __name__=='__main__':main()
