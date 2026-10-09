#!/usr/bin/env python3
"""The local stack (make dev, make verify): host Go processes and isolated real dependencies."""
import pathlib, sys
ROOT=pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT))
from prepare_tokenizer import prepare as prepare_tokenizer, requirements
from prepare_embeddings import prepare as prepare_embeddings, MODEL
import verify_report
import gotest
import guides
import normalizer_plugin
import ports
import subscription_plugin
import push_plugin
import connector_plugin
import archive_source
import fixture_plugin
import core_ingest_plugin
import queue_workers
import ingestion_plugin
import retrieval_plugin
import argparse, base64, json, math, os, pathlib, re, secrets, signal, subprocess, sys, time, urllib.request, uuid
from deploy import infrastructure as infrastructure_settings
GO=os.environ.get('GO','go')

def run(args, **kwargs):
    return subprocess.run(args, **{'check':True,'cwd':ROOT,**kwargs})

# macOS has no /proc: process checks ask ps, and TEI is reached on a published loopback port (THE-808).
MACOS=sys.platform=='darwin'

def ps(pid,field):
    """One ps field of pid ('' when there is no such process); macOS only."""
    return subprocess.run(['ps','-o',field+'=','-p',str(pid)],capture_output=True,text=True).stdout.strip()

def alive(pid):
    """Whether pid still runs: an exited or killed process that is not reaped yet (a zombie) does not."""
    if MACOS:return ps(pid,'stat')[:1] not in ('','Z')
    try:return pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()[0]!='Z'
    except (FileNotFoundError,IndexError):return False

# Obvious local test signing secret of the capture receiver destination.
CAPTURE_DESTINATION='local-receiver-capture'
CAPTURE_SECRET='whsec_'+base64.b64encode(b'local-test-signing-secret-capture').decode()
# Shortened webhook retry policy of the local harness, like its other short intervals (dev and verify;
# deployment defaults: 1s/5m/24h/10s). Verification reports it in report.json. The local receivers listen
# on loopback, so the harness also lifts the private-destination refusal (deployment default: refused).
# retry_initial stays 2 s: the first retry is jittered into [1 s, 2 s], so each attempt's webhook-timestamp (whole
# seconds) and therefore its signature differ, which acceptance checks; 1 s allowed two attempts in one second.
DELIVERY_OVERRIDES={'retry_initial':'2s','retry_max':'2s','window':'20s','allow_private_destinations':True}
# The worker physically prunes org_r's change journal after 2 s, every second (THE-697). The
# short retention is confined to org_r so org_a/org_b cursors keep the default seven days.
PRUNE_OVERRIDES={'interval':'1s','retention':'2s','organizations':['org_r'],'allow_short_retention':True}
# The short-retention API expires a cursor once the event after it is 1 s old, so the expiry acceptance
# tests wait about a second (THE-803). Catalog convergence reads its cursor every ~200 ms, well inside it.
SHORT_CHANGE_RETENTION='1s'
# Open change streams read the journal every 50 ms instead of 250 ms (THE-803).
CHANGE_STREAM_POLL='50ms'
OBSERVABILITY_OVERRIDES={'flush_interval':'200ms','record_query_text':True}
# Every harness service port comes from one allocator that never hands a port out twice and stays
# outside the kernel's ephemeral range, so two services cannot end up on one port (THE-728).
port=ports.allocate
PORT_KEYS=['api_port','probe_port','worker_probe_port','short_api_port','short_probe_port','receiver_port','graph_port','fake_x_port']

# Weaviate turns its shards read-only once the disk under its data is 90% full (its default
# DISK_USE_READONLY_PERCENTAGE); every index write then fails and ingestion retries until space
# frees up, so Records stop becoming searchable (THE-758). A verification refuses to start past
# DISK_LIMIT, which leaves room for what the run itself writes.
DISK_LIMIT=85

def docker_disk():
    """(Docker data root, percent used) of the disk holding Docker volumes, or None when it is not on
    this host (Docker Desktop keeps it in a VM). Computed like Weaviate: reserved blocks count as free."""
    root=subprocess.run(['docker','info','--format','{{.DockerRootDir}}'],capture_output=True,text=True).stdout.strip()
    if not root or not os.path.isdir(root):return None
    disk=os.statvfs(root)
    return root,round(100*(disk.f_blocks-disk.f_bfree)/disk.f_blocks,1)

class Stack:
    def __init__(self, name, source=ROOT):
        self.name=name
        # The source tree its engine, core plugins and dependencies come from: another checkout
        # when make eval compares two revisions (THE-874).
        self.source=pathlib.Path(source)
        # Verification refuses a nearly full disk; a dev stack only warns (THE-758).
        self.verifying=False
        self.directory=ROOT/'.scratch'/name
        self.directory.mkdir(parents=True,exist_ok=True,mode=0o700)
        self.directory.chmod(0o700)
        self.statefile=self.directory/'state.json'
        self.readiness={}
        # The latest process this command launched behind each probe port, in memory only: a readiness
        # wait stops as soon as it exits instead of polling a port nothing will open (THE-1138).
        self.launched={}
        if self.statefile.exists(): self.state=json.loads(self.statefile.read_text())
        else:
            self.state={'password':secrets.token_hex(24),'cursor_key':secrets.token_hex(32), 'admin':secrets.token_hex(32),'other':secrets.token_hex(32),'reader':secrets.token_hex(32),'scoped':secrets.token_hex(32),'denied':secrets.token_hex(32),'pids':[]}
        # Ports this stack already owns (a reloaded dev stack) are never handed to another service.
        ports.reserve([v for k,v in self.state.items() if k.endswith('_port')]+[self.state.get('custom_plugin',{}).get('port')])
        for key in PORT_KEYS:
            if key not in self.state:self.state[key]=port()
        for key in ['s3_access','s3_secret','writer','connector','connector_scoped','credential_key','configurer','keyless','demo','retention','operator','observer','backfiller']:
            self.state.setdefault(key,secrets.token_hex(24))
        self.save()
        identities={'identities':[{'name':'local-core','credentials':[{'accessKey':self.state['s3_access'],'secretKey':self.state['s3_secret']}],'actions':['Admin','Read','Write','List','Tagging']}]}
        # The read-only bind mount must be readable by Seaweed's container UID.
        # The enclosing 0700 directory keeps these credentials private on the host.
        s3file=self.directory/'s3.json';s3file.write_text(json.dumps(identities));s3file.chmod(0o644)
    def save(self):
        self.statefile.write_text(json.dumps(self.state));self.statefile.chmod(0o600)
    def compose(self,*args,**kwargs):
        files=['-f',str(self.source/'deploy/compose/compose.yaml')]+(['-f',str(self.source/'deploy/compose/compose.macos.yaml')] if MACOS else [])
        declaration=self.source/'deploy/infrastructure.json'
        if declaration.exists():
            overlay=infrastructure_settings.write_overlay(self.directory/'infrastructure.json',
                os.environ.get('QUIVR_INFRASTRUCTURE_PROFILE','small'),
                os.environ.get('QUIVR_INFRASTRUCTURE_OVERRIDES'),os.environ,declaration)
            files+=['-f',str(overlay)]
        return run(['docker','compose','-p',self.name,*files,*args],env={**os.environ,'QUIVR_DB_PASSWORD':self.state['password'],'QUIVR_LOCAL_ROOT':str(self.directory),'QUIVR_MODEL_ROOT':str(MODEL)},**kwargs)
    def config(self):
        address=self.compose('port','postgres','5432',capture_output=True,text=True).stdout.strip()
        s=self.state
        weaviate=self.compose('port','weaviate','8080',capture_output=True,text=True).stdout.strip()
        temporal=self.compose('port','temporal','7233',capture_output=True,text=True).stdout.strip()
        seaweed=self.compose('port','seaweed','8333',capture_output=True,text=True).stdout.strip()
        scope=lambda org,actions,corpora:dict(organization=org,actions=actions,corpora=corpora)
        if MACOS:tei=self.compose('port','tei','80',capture_output=True,text=True).stdout.strip()
        else:
            tei_container=self.compose('ps','-q','tei',capture_output=True,text=True).stdout.strip()
            tei=run(['docker','inspect',tei_container,'--format','{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'],capture_output=True,text=True).stdout.strip()+':80'
        # The core.ingest pin (scripts/connector_plugin.py FIRST_PARTY) embeds through this TEI.
        s['tei_url']='http://'+tei;self.save()
        cfg=dict(tei_url='http://'+tei,weaviate_url='http://'+weaviate,temporal_address=temporal,s3=dict(endpoint='http://'+seaweed,access_key=s['s3_access'],secret_key=s['s3_secret'],bucket='quivr-content'),log_directory=str(self.directory),database_url=f"postgres://quivr:{s['password']}@{address}/quivr?sslmode=disable",listen=f"127.0.0.1:{s['api_port']}",probe_listen=f"127.0.0.1:{s['probe_port']}",cursor_key=s['cursor_key'],credential_key=s['credential_key'],connector_min_interval='1s',
            # api and worker follow a plugin activation within this delay (THE-781).
            plugin_plan_poll='200ms',
            # Verification exercises snapshots promptly; dev keeps deployment pacing.
            retrieval=dict(coverage_refresh='100ms' if self.verifying else '10s'),
            queue_observation=dict(refresh_interval='1s' if self.verifying else '15s'),
            change_stream_poll=CHANGE_STREAM_POLL,
            # Work pinned to a plan whose plugin left it and cannot be reached stops after two attempts (THE-782).
            pinned_plugin_attempts=2,
            # Backfills fill one Version a second, so the acceptance pauses one halfway; a paused one checks every 200ms (THE-784).
            backfill=dict(rate=1,poll='200ms'),
            # Push connector instances (x_list webhook mode) register webhooks here; the fake X calls it on loopback.
            public_url=f"http://127.0.0.1:{s['api_port']}",
            keys={
            s['admin']:scope('org_a',['corpora:archive','corpora:rename','audit:read','corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write','observability:read'],['*']),
            s['other']:scope('org_b',['corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write','connectors:read','connectors:write','connectors:admin','connector:push'],['*']),
            # Connector acceptance owns org_c so its scheduled load cannot skew org_a/org_b scenarios.
            s['connector']:scope('org_c',['corpora:read','corpora:write','content:read','content:write','search:query','changes:read','connectors:read','connectors:write','blobs:read'],['*']),
            s['connector_scoped']:scope('org_c',['connectors:read','connectors:write'],['corpus_not_granted']),
            # The browser demo (scripts/demo.py) owns org_d: its connectors keep polling without touching acceptance Organizations.
            # Its keyword alerts need the monitoring rights to read Matches through the API;
            # its read-only Admin tab (THE-796) needs observability:read.
            s['demo']:scope('org_d',['corpora:read','corpora:write','content:read','content:write','search:query','changes:read','connectors:read','connectors:write','monitoring:read','monitoring:write','observability:read'],['*']),
            # Change-journal prune acceptance owns org_r, the only Organization the harness prunes.
            s['retention']:scope('org_r',['corpora:read','corpora:write','content:read','content:write','changes:read'],['*']),
            s['reader']:scope('org_a',['corpora:read'],['*']),
            # The deployment operator: reads the plugin registry (plugins:admin), which no Organization key gets,
            # and the admin views (observability:read).
            s['operator']:scope('org_ops',['plugins:admin','observability:read','queues:read'],['*']),
            # An operator of org_a: backfills its Corpora and promotes vector spaces (THE-784).
            s['backfiller']:scope('org_a',['plugins:admin','operations:read','operations:write'],['*']),
            # Observability acceptance owns org_o: its stats reads see only its own ingestion and searches.
            s['observer']:scope('org_o',['corpora:read','corpora:write','content:read','content:write','search:query','observability:read'],['*']),
            s['scoped']:scope('org_a',['corpora:read','corpora:write','content:read','content:write','search:query','blobs:read','blobs:write','changes:read','monitoring:read','monitoring:write','projections:rebuild','operations:read','operations:write'],[s.get('scoped_id','corpus_not_granted')]),
            s['writer']:scope('org_a',['content:write'],['*']),
            s['denied']:scope('org_a',['content:read'],['*']),
            # Corpus writer without operations:write: cannot change retrieval configuration.
            s['configurer']:scope('org_a',['corpora:read','corpora:write'],['*']),
            # Each doc page with runnable blocks replays in its own Organization (scripts/guides.py).
            **guides.keys(self)},
            # One deployment-configured webhook destination per Organization. These are obvious
            # local test values; real deployments reference the signing secret through secret_env.
            destinations={'local-receiver-org-a':dict(organization='org_a',url='http://127.0.0.1:9/local-receiver-org-a',secret='whsec_'+base64.b64encode(b'local-test-signing-secret-org-a!').decode()),
                          'local-receiver-org-b':dict(organization='org_b',url='http://127.0.0.1:9/local-receiver-org-b',secret='whsec_'+base64.b64encode(b'local-test-signing-secret-org-b!').decode()),
                          # The keyless restart creates alerts in org_k (TestKeylessRefusesDescribedAlerts); nothing is delivered there.
                          'local-receiver-org-k':dict(organization='org_k',url='http://127.0.0.1:9/local-receiver-org-k',secret='whsec_'+base64.b64encode(b'local-test-signing-secret-org-k!').decode()),
                          # Signed-delivery acceptance runs its own receiver on this port while it executes.
                          CAPTURE_DESTINATION:dict(organization='org_a',url=f"http://127.0.0.1:{s['receiver_port']}/capture",secret=CAPTURE_SECRET),
                          # One per doc page with runnable blocks, in that page's Organization ($QUIVR_DESTINATION).
                          **guides.destinations(self)},
            delivery=DELIVERY_OVERRIDES,
            # The pinned external normalizer: pdf-text for application/pdf (make dev default), the
            # `quivr plugin init` template for text/markdown, or none (scripts/normalizer_plugin.py).
            plugin=normalizer_plugin.pin(self),
            # The keyword alerts plugin and the alert-rule template pinned beside it (scripts/subscription_plugin.py), and in
            # verification the sample connector plugin (scripts/connector_plugin.py). The shared fake plugin
            # supplies the scripted connector and notification-mechanics evaluator.
            plugins=push_plugin.pins(self)+subscription_plugin.pins(self)+connector_plugin.pins(self)+connector_plugin.first_party_pins(self)+fixture_plugin.pins(self),
            # Counts reach the stats reads within 200 ms; query text is recorded so top queries can be read back.
            observability=OBSERVABILITY_OVERRIDES)
        f=self.directory/'config.json';f.write_text(json.dumps(cfg));f.chmod(0o600)
        (self.directory/'tokenizer-provenance.json').write_text((ROOT/'plugins/core-ingest/profile.json').read_text())
        # A second API over the same database with a short change retention proves public cursor expiry.
        short=self.directory/'short-retention.json';short.write_text(json.dumps({**cfg,'listen':f"127.0.0.1:{s['short_api_port']}",'probe_listen':f"127.0.0.1:{s['short_probe_port']}",'change_retention':SHORT_CHANGE_RETENTION}));short.chmod(0o600)
        worker=self.directory/'worker.json';cfg['probe_listen']=f"127.0.0.1:{s['worker_probe_port']}";worker.write_text(json.dumps({**cfg,'change_prune':PRUNE_OVERRIDES}));worker.chmod(0o600)
        # Keyless variant (THE-691): same stack without credential_key, its own Organization
        # and log directory. Only verify_keyless uses it; the harness always returns to config.json.
        keyless_logs=self.directory/'keyless';keyless_logs.mkdir(mode=0o700,exist_ok=True)
        keyless={k:v for k,v in cfg.items() if k!='credential_key'}
        # The keyless core also pins the alerts plugin as an installation without a TypeSafe key: keyword alerts only.
        keyless.update(log_directory=str(keyless_logs),plugins=push_plugin.pins(self)+subscription_plugin.pins(self,described='off')+connector_plugin.first_party_pins(self)+fixture_plugin.pins(self),keys={s['keyless']:scope('org_k',['corpora:read','corpora:write','content:read','content:write','search:query','changes:read','connectors:read','connectors:write','monitoring:read','monitoring:write'],['*'])})
        for name,probe in [('keyless.json','probe_port'),('keyless-worker.json','worker_probe_port')]:
            f=self.directory/name;f.write_text(json.dumps({**keyless,'probe_listen':f"127.0.0.1:{s[probe]}"}));f.chmod(0o600)
    def running(self):
        """Whether this project's PostgreSQL container is up; make migrate needs a started project."""
        return bool(self.compose('ps','-q','--status','running','postgres',capture_output=True,text=True).stdout.strip())
    def migrate(self):
        if not self.running():
            raise RuntimeError(f'project {self.name} is not running; start it with `make dev` before `make migrate`')
        self.config()
        with (self.directory/'migrate-startup.log').open('w') as log:
            run([str(self.directory/'quivr'),'migrate'],env={**os.environ,'QUIVR_CONFIG':str(self.directory/'config.json')},stdout=log,stderr=log)
    def shutdown_grace(self,config):
        # Match positive Go duration units and env precedence used by processSettings.
        value=os.environ.get('QUIVR_SHUTDOWN_GRACE') or json.loads((self.directory/config).read_text()).get('shutdown_grace') or '60s'
        if not isinstance(value,str):raise RuntimeError('invalid shutdown_grace')
        units={'ns':1e-9,'us':1e-6,'µs':1e-6,'μs':1e-6,'ms':.001,'s':1,'m':60,'h':3600}
        parts=re.findall(r'(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:ns|us|µs|μs|ms|s|m|h)',value.removeprefix('+'))
        if not parts or ''.join(parts)!=value.removeprefix('+'):raise RuntimeError('invalid shutdown_grace')
        seconds=sum(float(re.match(r'[0-9.]+',part)[0])*units[re.search(r'[^0-9.]+',part)[0]] for part in parts)
        if not math.isfinite(seconds) or seconds<=0:raise RuntimeError('invalid shutdown_grace')
        return seconds
    def spawn(self,command,config):
        grace=self.shutdown_grace(config)
        with (self.directory/(command+'-startup.log')).open('a') as log:
            p=subprocess.Popen([str(self.directory/'quivr'),command],cwd=ROOT,env={**os.environ,**connector_plugin.engine_environment(self,push_plugin.engine_environment(self)),'QUIVR_CONFIG':str(self.directory/config)},stdout=log,stderr=log,start_new_session=True)
        self.state['pids'].append(p.pid)
        self.state.setdefault('shutdown_graces',{})[str(p.pid)]=grace
        if command in ('api','worker'):self.state[command+'_pid']=p.pid
        listen=json.loads((self.directory/config).read_text()).get('probe_listen')
        if isinstance(listen,str):self.launched[int(listen.rsplit(':',1)[1])]=p
        self.save()
    def await_ready(self,key,timeout=20):
        """Bounded readiness wait; a timeout names the probe, its last answer and the logs to read. A process
        launched behind the probe that exits first fails the wait at once with its status and last log lines."""
        probe={'probe_port':'api','worker_probe_port':'worker','short_probe_port':'short-api','queue_bulk_probe_port':'worker'}.get(key,key)
        url=f"http://127.0.0.1:{self.state[key]}/readyz";start=time.monotonic();last='no answer'
        while True:
            answered=False
            try:
                with urllib.request.urlopen(url,timeout=1) as r:
                    answered=r.status==204
                    last=f'HTTP {r.status}'
            except urllib.error.HTTPError as error:last=f'HTTP {error.code}: {error.read(200).decode(errors="replace").strip()}'
            except OSError as error:last=type(error).__name__
            # Checked before a 204 is accepted: a previous process still on the port must not answer for it.
            if (process:=self.launched.get(self.state[key])) is not None and (code:=process.poll()) is not None:
                self.readiness[probe]={'ready':False,'exited':code,'waited_seconds':round(time.monotonic()-start,3)}
                self.save_readiness()
                log=self.directory/f'{probe}-startup.log'
                tail=log.read_text(errors='replace').splitlines()[-10:] if log.exists() else []
                raise RuntimeError(f'{probe} exited with status {code} before it was ready at {url}; last lines of {log}:\n'+'\n'.join(line[:300] for line in tail))
            if answered:break
            if time.monotonic()-start>timeout:
                self.readiness[probe]={'ready':False,'last':last,'waited_seconds':round(time.monotonic()-start,3)}
                self.save_readiness()
                raise RuntimeError(f'{probe} readiness timed out after {timeout}s at {url} (last: {last}); inspect {self.directory}/{probe}-startup.log')
            time.sleep(.1)
        self.readiness[probe]={'ready':True,'waited_seconds':round(time.monotonic()-start,3)}
        self.save_readiness()
    def probe(self,key):
        """Status of one /readyz probe, or None when nothing answers."""
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{self.state[key]}/readyz",timeout=3) as r:return r.status
        except urllib.error.HTTPError as error:return error.code
        except OSError:return None
    def readiness_split(self):
        """During a search outage: API ready (204), worker live but not ready (503)."""
        deadline=time.monotonic()+15
        while (observed:={'api':self.probe('probe_port'),'worker':self.probe('worker_probe_port')})!={'api':204,'worker':503}:
            assert time.monotonic()<deadline,f'readiness must separate acceptance from the outage: {observed}'
            time.sleep(.2)
        self.readiness['during_search_outage']=observed;self.save_readiness()
    def save_readiness(self):
        (self.directory/'readiness.json').write_text(json.dumps(self.readiness,indent=2))
    def start_fake_graph(self):
        if getattr(self,'fake_graph',None) is None:
            from fake_api import Fake
            self.fake_graph=Fake('graph', self.state['graph_port'])
    def start_processes(self,keyless=False):
        self.start_fake_graph()
        for command,config in [('api','keyless.json' if keyless else 'config.json'),('worker','keyless-worker.json' if keyless else 'worker.json')]:self.spawn(command,config)
        for key in ['probe_port','worker_probe_port']:self.await_ready(key)
    def owns_process(self,pid):
        try:
            cmd=ps(pid,'comm').encode() if MACOS else pathlib.Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')[0]
            return cmd==str(self.directory/'quivr').encode()
        except (FileNotFoundError,ProcessLookupError):return False
    def signal_owned(self,pid,sig):
        # Refuse to signal a reused PID belonging to any unrelated program.
        try:
            if self.owns_process(pid):os.kill(pid,sig)
        except ProcessLookupError:pass
    def stop_worker(self):
        """Stop only the worker; the API keeps accepting durable commands."""
        pid=self.state.pop('worker_pid',None)
        if pid is None:raise RuntimeError('worker not tracked')
        self.signal_owned(pid,signal.SIGKILL)
        self.state['pids']=[p for p in self.state['pids'] if p!=pid];self.save()
        # The harness spawned the worker and never reaps it, so once killed it stays a zombie: stopped, holding
        # no port or lease. Waiting for /proc/<pid> to vanish would always run the whole deadline (THE-755).
        deadline=time.monotonic()+10
        while alive(pid) and time.monotonic()<deadline:time.sleep(.05)
    def start_worker(self):
        self.spawn('worker','worker.json');self.await_ready('worker_probe_port')
    def stop_api(self):
        """Stop only the api, as a rolling deploy does: the worker keeps processing."""
        pid=self.state.pop('api_pid',None)
        if pid is None:raise RuntimeError('api not tracked')
        self.signal_owned(pid,signal.SIGTERM)
        self.state['pids']=[p for p in self.state['pids'] if p!=pid];self.save()
        deadline=time.monotonic()+10
        while alive(pid) and time.monotonic()<deadline:time.sleep(.05)
    def start_api(self):
        self.spawn('api','config.json');self.await_ready('probe_port')
    def start_short_retention_api(self):
        # Its pins are the stack's current ones: a start with other pins would apply them to the plan all
        # processes follow (THE-781).
        cfg=json.loads((self.directory/'config.json').read_text());s=self.state
        short=self.directory/'short-retention.json';short.write_text(json.dumps({**cfg,'listen':f"127.0.0.1:{s['short_api_port']}",'probe_listen':f"127.0.0.1:{s['short_probe_port']}",'change_retention':SHORT_CHANGE_RETENTION}));short.chmod(0o600)
        grace=self.shutdown_grace('short-retention.json')
        with (self.directory/'short-api-startup.log').open('w') as log:
            p=subprocess.Popen([str(self.directory/'quivr'),'api'],cwd=ROOT,env={**os.environ,**connector_plugin.engine_environment(self,push_plugin.engine_environment(self)),'QUIVR_CONFIG':str(self.directory/'short-retention.json')},stdout=log,stderr=log,start_new_session=True)
        self.state['pids'].append(p.pid);self.state.setdefault('shutdown_graces',{})[str(p.pid)]=grace;self.launched[s['short_probe_port']]=p;self.save()
        self.await_ready('short_probe_port')
    def stop_processes(self):
        for pid in self.state['pids']:self.signal_owned(pid,signal.SIGTERM)
        # Honor launch-time grace even after reload; ignore unrelated reused PIDs.
        # Keep ownership on timeout so cleanup can still find outstanding children.
        graces=self.state.get('shutdown_graces',{})
        deadline=time.monotonic()+max([60]+[graces.get(str(pid),60) for pid in self.state['pids'] if self.owns_process(pid)])+10
        while any(self.owns_process(pid) and alive(pid) for pid in self.state['pids']):
            if time.monotonic()>=deadline:raise RuntimeError('process shutdown timed out; tracked children still alive')
            time.sleep(.05)
        self.state['pids']=[];self.state.pop('worker_pid',None);self.state.pop('api_pid',None);self.state.pop('shutdown_graces',None);self.save()
    def check_disk(self):
        disk=docker_disk()
        if disk is None:return
        root,used=disk
        self.readiness['docker_disk_used_percent']=used;self.save_readiness()
        if used>=DISK_LIMIT:
            message=f'the disk holding Docker volumes ({root}) is {used}% full; Weaviate turns read-only at 90%, so Records would stop becoming searchable. Free disk space below {DISK_LIMIT}%.'
            if self.verifying:raise RuntimeError(message)
            print('Warning: '+message,flush=True)
    def up(self):
        self.check_disk()
        self.stop_processes()
        prepare_tokenizer()
        (self.directory/'embedding-provenance.json').write_text(json.dumps(prepare_embeddings(),indent=2))
        run([GO,'build','-o',str(self.directory/'quivr'),'./cmd/quivr'],cwd=self.source)
        self.start_dependencies()
        normalizer_plugin.prepare(self);subscription_plugin.prepare(self);fixture_plugin.prepare(self)
        self.migrate();self.migrate();normalizer_plugin.start(self);subscription_plugin.start(self);push_plugin.start(self);connector_plugin.start_first_party(self);fixture_plugin.start(self);self.start_processes()
    def start_dependencies(self,attempts=2):
        """Start the pinned dependencies with bounded readiness. A dependency that crashes while
        starting (SeaweedFS 4.45 can hit a raft map race when restarting on existing data) gets one
        more bounded attempt; every retry is recorded in readiness.json, never hidden."""
        for attempt in range(1,attempts+1):
            try:
                self.compose('up','-d','--wait','--wait-timeout','180');return
            except subprocess.CalledProcessError as error:
                crashed=self.compose('ps','--all','--status','exited','--format','{{.Service}}',capture_output=True,text=True).stdout.split()
                self.readiness.setdefault('dependency_start_retries',[]).append({'attempt':attempt,'exited':crashed})
                self.save_readiness()
                # Only a crashed dependency is retried; a healthcheck timeout fails at once.
                if not crashed or attempt==attempts:raise RuntimeError(f'dependencies not ready after {attempt} bounded attempt(s) (exited: {crashed or "none"}); inspect services.json and the service logs') from error
    def tests(self,pattern,extra_env=None):
        s=self.state
        env={**os.environ,**(extra_env or {}),'QUIVR_TEST_CAPTURES':str(self.directory),'QUIVR_TEST_BINARY':str(self.directory/'quivr'),'QUIVR_TEST_URL':f"http://127.0.0.1:{s['api_port']}",**{'QUIVR_TEST_'+k.upper():s[k] for k in ['admin','other','reader','scoped','denied','writer','connector','connector_scoped','configurer','keyless','retention','operator','observer','backfiller']},'QUIVR_TEST_SHORT_RETENTION_URL':f"http://127.0.0.1:{s['short_api_port']}",'QUIVR_TEST_RECEIVER_ADDR':f"127.0.0.1:{s['receiver_port']}",'QUIVR_TEST_RECEIVER_SECRET':CAPTURE_SECRET,'QUIVR_TEST_WORKER_PROBE_URL':f"http://127.0.0.1:{s['worker_probe_port']}",'QUIVR_TEST_FAKE_GRAPH_URL':f"http://127.0.0.1:{s['graph_port']}",'QUIVR_TEST_FAKE_X_URL':f"http://127.0.0.1:{s['fake_x_port']}"}
        self.go_test(['-count=1','-run',pattern,'./tests/acceptance'],env,'acceptance')
    def go_test(self,args,env,name,cwd=ROOT):
        """go test with its text in <name>.log; failed tests and every test's duration reach the report (THE-755)."""
        record=getattr(self,'steps',None) and self.steps.record_tests
        try:results=gotest.run(GO,args,cwd,env,self.directory/(name+'.log'),self.directory/(name+'.jsonl'))
        except gotest.Failed as failed:
            if record and failed.results:record(failed.results.tests())
            raise
        if record:record(results.tests())
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
        # Readiness separates durable acceptance from a downstream outage (THE-662): the API
        # stays ready while the worker reports the lost search dependency, without a restart.
        self.readiness_split()
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
    def verify_keyless(self):
        """Restart api and worker without credential_key, prove the keyless core, then restore keyed mode."""
        start=time.monotonic();status='failed'
        self.stop_processes()
        try:
            self.start_processes(keyless=True)
            for command in ['api','worker']:
                lines=(self.directory/'keyless'/(command+'.log')).read_text().count('credential deposits disabled')
                assert lines==1,f'{command} logged credential deposits disabled {lines} times'
            self.tests('TestKeyless',{'QUIVR_TEST_KEYLESS_MODE':'1',**subscription_plugin.environment(self)})
            status='passed'
        finally:
            (self.directory/'keyless-report.json').write_text(json.dumps({'status':status,'duration_seconds':round(time.monotonic()-start,3)}))
            # Never leave the harness keyless for a later step, even when the scenario fails. Like the
            # other restart scenarios this restores api and worker only, not the short-retention API.
            self.stop_processes();self.start_processes()
    def capture(self):
        # Last metrics of the processes still running; bounded labels, no secrets.
        for name,key in [('api','probe_port'),('worker','worker_probe_port')]:
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{self.state[key]}/metrics",timeout=2) as r:(self.directory/f'metrics-{name}.txt').write_bytes(r.read())
            except OSError:pass
        with (self.directory/'services.json').open('w') as out:self.compose('ps','--all','--format','json',stdout=out)
        for service in ['postgres','temporal','seaweed','weaviate','tei']:
            with (self.directory/(service+'.log')).open('w') as log:self.compose('logs','--no-color',service,stdout=log,stderr=log)
    def down(self,reset=False):
        """Stop this project's processes and containers. reset also deletes its volumes and the
        state bound to that data; generated credentials and ports are kept and nothing is
        started again (make dev initializes a fresh schema)."""
        if hasattr(self,"fake_x"): self.fake_x.close()
        if hasattr(self,"fake_graph"): self.fake_graph.close()
        normalizer_plugin.stop(self);subscription_plugin.stop(self);push_plugin.stop(self);connector_plugin.stop(self);connector_plugin.stop_first_party(self);fixture_plugin.stop(self);self.stop_processes();self.compose('down',*(['--volumes'] if reset else []))
        if reset:
            for key in ['scoped_id','worker_pid','api_pid']:self.state.pop(key,None)
            self.save()


def api_call(stack,path,expected=200):
    """One authenticated public read, asserting its status; returns the JSON body."""
    req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}{path}",headers={'Authorization':'Bearer '+stack.state['admin']})
    try:response=urllib.request.urlopen(req,timeout=5)
    except urllib.error.HTTPError as error:response=error
    with response:
        body=json.load(response)
        assert response.status==expected,(path,response.status,body)
        return body

def persistence(stack):
    stack.tests('TestCorpusPersistsAndReplays')
    original_id=api_call(stack,'/v0/corpora')['items'][0]['corpus_id']
    (stack.directory/'original-corpus-id.txt').write_text(original_id)
    # Restart all application processes and PostgreSQL; assert via HTTP again.
    stack.stop_processes();stack.compose('restart','postgres');stack.compose('up','-d','--wait','--wait-timeout','180');stack.migrate();stack.start_processes()
    assert api_call(stack,'/v0/corpora/'+original_id)['corpus_id']==original_id
    stack.tests('TestCorpusPersistsAndReplays')
    stack.state['scoped_id']=api_call(stack,'/v0/corpora')['items'][0]['corpus_id']
    stack.save();stack.stop_processes();stack.config();stack.start_processes()

def adapters(stack):
    stack.stop_processes()
    env={**os.environ,'QUIVR_ADAPTER_CONFIG':str(stack.directory/'config.json')}
    stack.go_test(['-count=1','./internal/adapters/weaviate/...','./internal/adapters/tei/...','./internal/adapters/s3/...','./internal/processing/...'],env,'adapters')
    # adapter-postgres owns the database-only tests; this one also needs TEI and S3.
    stack.go_test(['-count=1','-run','^TestDurableEmbeddingConflictAndAtomicEnrichment$','./internal/adapters/postgres/...'],env,'adapters-postgres')
    stack.start_processes()

def delivery_restart(stack):
    # A Delivery with one failed attempt converges after the worker is killed and restarted.
    stack.tests('TestDeliveryRestartBefore');stack.stop_worker();stack.start_worker();stack.tests('TestDeliveryRestartAfter')

def journey(stack,steps):
    """The assembled public journey (THE-662) around a real worker outage, which the
    failure drill then explains from probes, metrics and logs alone."""
    import failure_drill
    steps.run('journey_before_restart',stack.tests,'^TestJourneyBeforeRestart$')
    steps.run('stop_worker',stack.stop_worker)
    try:
        steps.run('journey_worker_stopped',stack.tests,'^TestJourneyWorkerStopped$')
        drill=steps.run('failure_drill_during_outage',failure_drill.during,stack)
    finally:steps.run('start_worker',stack.start_worker)
    steps.run('journey_after_restart',stack.tests,'^TestJourneyAfterRestart$')
    steps.run('failure_drill_after_restart',failure_drill.after,stack,drill)

def connectors(stack):
    # Connector acquisition keeps polling on its schedule; run it after every
    # timed scenario, in its own Organization, then prove restart resumption.
    # The x_list kind (plugins/x-list, pinned with the fake's api_endpoint)
    # polls the same Go fake binary used by the plugin unit suites.
    from fake_api import Fake
    stack.fake_x = Fake("x", stack.state["fake_x_port"])
    stack.tests('TestConnector')

def step(name,fn,*args):
    """One named verification step: fn(stack,*args)."""
    return lambda stack,steps:steps.run(name,fn,stack,*args)

def acceptance(name,pattern):
    """A step that runs the acceptance tests matching pattern against the part's stack."""
    return step(name,Stack.tests,pattern)

def contract_python():
    """The Python that validates captures: CONTRACT_PYTHON, or the contracts venv (created when a part runs without make check)."""
    python=os.environ.get('CONTRACT_PYTHON')
    if python:return python
    venv_dir=ROOT/'.scratch/contracts/venv'
    if not (venv_dir/'bin/python').exists():run(['python3','-m','venv',str(venv_dir)])
    # Idempotent and quick when satisfied; recovers a half-installed venv and follows requirement changes.
    run([str(venv_dir/'bin/pip'),'-q','install','-r','contracts/http/v0/checks/requirements.txt'])
    return str(venv_dir/'bin/python')

def validate_captures(stack):
    run([contract_python(),'scripts/validate_captures.py',str(stack.directory)])

def lifecycle_required():
    """Only CI with a known unchanged local.py may omit the expensive lifecycle proof."""
    base=os.environ.get('QUIVR_VERIFY_BASE')
    if not base:return True
    changed=subprocess.run(['git','diff','--quiet',base,'HEAD','--','scripts/local.py','scripts/lifecycle.py','deploy/reset.py','deploy/reset_support.py','deploy/reset_storage.py','deploy/compose/reset.py','deploy/railway/reset.py'],cwd=ROOT)
    return changed.returncode!=0  # A missing/invalid base is uncertainty: run the proof.

def parts():
    """The full verification in parts (THE-755). Each part starts its own isolated stack and runs its
    steps in order, so a step sees only the state its own part built. CI runs the parts in parallel
    (.github/workflows/verify.yml); `make verify` runs them one after another. To add a step, add one
    line to the part whose state it needs: acceptance('name','^TestPattern') or step('name',fn)."""
    from hosted_embed_plugin import verify as verify_hosted_embed, verify_redeploy as verify_hosted_redeploy
    from embedding_outage import verify as verify_embedding_outage
    from first_search import verify as verify_first_search
    from tracing import verify as verify_tracing
    from rebuild_recovery import verify as verify_rebuild_recovery
    from operation_control import verify as verify_operation_control
    from connector_restart import verify as verify_connector_restart
    from m365_restart import verify as verify_m365_restart
    from connector_x_restart import verify as verify_connector_x_restart
    from connector_x_push import verify as verify_connector_x_push
    from lifecycle import verify as verify_lifecycle
    from weaviate_upgrade import verify as verify_weaviate_upgrade
    # Every part first restarts PostgreSQL under load and grants the scoped key its Corpus.
    setup=[step('persistence_across_restart',persistence)]
    return {
        # Core contracts, adapters, outages of every dependency, then the change feed and the CLI.
        'core':setup+[
            acceptance('core_acceptance','TestAuthorization|TestValidation|TestPagination|TestConcurrent|TestInline|TestStructuredManifest|TestManifest|TestWithdrawal|TestCorrection|TestLexical|TestLong|TestSemantic|TestUpload|TestBatch'),
            # A cold query encoder and a fresh api answer the first semantic and hybrid searches (THE-813).
            step('first_search_after_start',verify_first_search),
            step('weaviate_persistence_upgrade',verify_weaviate_upgrade),
            step('adapter_integration',adapters),
            # Tokenizer-only golden parity and certification; full vectors run in ingest-parity nightly.
            step('core_ingest_plugin',core_ingest_plugin.verify),
            step('ingestion_outages',Stack.ingestion_outages),
            step('embedding_outage',verify_embedding_outage),
            step('rebuild_recovery',verify_rebuild_recovery),
            step('operation_control',verify_operation_control),
            # Change-feed, catalog resync and rebuild tests add Corpora and ingestion load; run them after
            # order-sensitive acceptance and timed outage scenarios. The outages kill every process, so
            # the short-retention API starts after them.
            step('short_retention_api',Stack.start_short_retention_api),
            acceptance('corpus_lifecycle','^TestCorpusLifecycle$'),
            acceptance('changes_catalog_rebuild','TestChange|TestCatalog|TestRebuild|TestRetrievalConfiguration|TestFacets|TestEnrichedVersions'),
            step('newsml_normalizer_pin',normalizer_plugin.switch,'newsml-g2'),
            acceptance('metadata_filters','^TestMetadataFiltersAcrossCorpora$'),
            step('template_normalizer_pin',normalizer_plugin.switch,'template'),
            # The built quivr binary searches through the public API and keeps its offline plugin tools (THE-702).
            acceptance('cli','^TestCLI'),
            # Plugin calls, searches and steps counted and read back through the admin stats (THE-795).
            acceptance('observability','^TestObservabilityStats$'),
            step('end_to_end_tracing',verify_tracing),
            step('validate_captures',validate_captures)],
        # Monitoring, webhook delivery across a worker restart, and the assembled public journey (THE-662).
        'monitoring':setup+[
            step('short_retention_api',Stack.start_short_retention_api),
            acceptance('monitoring','TestMonitoring'),
            step('delivery_worker_restart',delivery_restart),
            journey,
            step('validate_captures',validate_captures)],
        # Normalizer and alert-rule plugins, the keyless core, then the harness lifecycle.
        'plugins':[
            # Inspect the pristine registry before redeploy leaves historical registrations behind.
            acceptance('plugin_registry','^TestPluginRegistry$'),
            step('hosted_build_redeploy',verify_hosted_redeploy)]+setup+[
            # A routed Markdown Blob is normalized by the pinned plugin and its outline extension is mapped into search;
            # then invalid pins are refused, and with the plugin stopped the processes stay healthy and a rebuild needs no plugin.
            acceptance('normalizer','TestNormalizerMakesRoutedBlobsSearchable|TestNormalizerExtensionsFeedRetrievalMappings'),
            step('normalizer_startup',normalizer_plugin.verify),
            acceptance('normalizer_rebuild_without_plugin','TestNormalizerRebuildWithoutPlugin'),
            # With the plugin down, routed work waits and the platform stays healthy; then a controllable
            # test plugin is pinned to observe every failure class and the optional-route fallback.
            step('normalizer_outage',normalizer_plugin.outage),
            step('normalizer_failures',normalizer_plugin.failures),
            # The Go SDK sample ingestion plugin pinned beside them: a Corpus rebuilt onto its named spaces is
            # segmented, embedded and searched through it, served and evaluation spaces both covered.
            step('ingestion_plugin',ingestion_plugin.verify),
            # The Go SDK sample retrieval plugin pinned beside them: it ranks every search from keyword and vector
            # candidates the engine served, under the profiles it declares.
            step('retrieval_plugin',retrieval_plugin.verify),
            # v0 pins one plugin: switch to the reference pdf-text plugin (the make dev default) for PDFs.
            step('pdf_normalizer_pin',normalizer_plugin.switch,'pdf-text'),
            step('ingestion_source_routes',ingestion_plugin.verify_routes),
            acceptance('pdf_normalizer','TestPDF'),
            # Alerts decided by the pinned alert-rule template, next to pdf-text: one webhook per match,
            # none for a non-match, 422 for an invalid expression, metadata rules; then a rule-plugin outage
            # delays evaluation, which completes after the restart.
            step('alert_plugin',subscription_plugin.verify),
            # Keyword alerts decided by plugins/alerts: the evidence names the matched terms, a filter alone alerts.
            step('keyword_alerts',subscription_plugin.keywords),
            step('vector_alerts',subscription_plugin.vectors),
            # Described alerts judged through the fake System One server, never TypeSafe: one call per article for every described alert.
            step('described_alerts',subscription_plugin.described),
            step('alert_plugin_outage',subscription_plugin.outage),
            # The template's 0.2.0 is activated without restart: existing alerts stay on 0.1.0 until migrated,
            # then a rollback serves 0.1.0 again and the alerts are migrated back (THE-805).
            step('alert_plugin_upgrade',subscription_plugin.upgrade),
            # Runnable guide blocks, each page in its own Organization (scripts/guides.py).
            step('hosted_embedding_plugin',verify_hosted_embed),
            step('runnable_guides',guides.verify),
            # The keyless worker would fail credentialed instances of earlier scenarios.
            step('keyless_core',Stack.verify_keyless),
            step('validate_captures',validate_captures),
            # Last: stop/migrate/reset semantics on this isolated project.
            *([step('lifecycle',verify_lifecycle)] if lifecycle_required() else [])],
        # Connector acquisition keeps polling on its schedule, in its own Organization; then restart resumption.
        'connectors':setup+[
            # Connectors ingest PDF attachments; they run on the make dev default, the pdf-text pin.
            step('pdf_normalizer_pin',normalizer_plugin.switch,'pdf-text'),
            step('connectors',connectors),
            # Connector kinds from a pinned plugin: collect and resume, then a plugin outage and its recovery.
            step('collector_plugin',connector_plugin.verify),
            step('queue_workers',queue_workers.verify),
            step('archive_source',archive_source.verify),
            step('connector_restart',verify_connector_restart),
            step('m365_restart',verify_m365_restart),
            step('x_restart',lambda stack:verify_connector_x_restart(stack,f"http://127.0.0.1:{stack.state['fake_x_port']}")),
            # X webhook deliveries while the x-list plugin is down: 503 to X, then polling catches up.
            step('x_push_outage',lambda stack:verify_connector_x_push(stack,f"http://127.0.0.1:{stack.state['fake_x_port']}")),
            acceptance('install_apply','^TestInstallApply$'),
            # Last, since it activates them: pdf-text and the RSS connector registered with their own fixtures (THE-807).
            acceptance('plugin_own_fixtures','^TestPluginRegistrationWithItsFixtures$'),
            step('validate_captures',validate_captures)],
    }

# The browser demo (scripts/demo.py) runs on its own stack; it is the last part of `make verify`.
DEMO='demo'

def extra_parts():
    """Explicit stack proofs outside the default PR lane."""
    from lifecycle import verify as verify_lifecycle
    return {'ingest-parity':[step('core_ingest_vector_parity',core_ingest_plugin.parity)],
            'lifecycle':[step('lifecycle',verify_lifecycle)]}

def verify(stack,steps,part):
    """Every step of one part, in order; each feature keeps its own tests."""
    for entry in (parts()|extra_parts())[part]:entry(stack,steps)

def preparation(stack,steps):
    """Cold preparation (model/tokenizer download) is reported apart from the warm stack start."""
    try:embedding=json.loads((stack.directory/'embedding-provenance.json').read_text())
    except (OSError,ValueError):embedding={}
    start=next((s.get('seconds') for s in steps.items if s['step']=='start_stack'),None)
    return {'model_prepare_seconds':embedding.get('prepare_seconds'),'model_downloaded_bytes':embedding.get('downloaded_bytes'),'start_stack_seconds':start,
            'note':'start_stack includes preparation, image pulls on a cold cache, build, Compose readiness and migrations; no startup-time claim'}

def finish(stack,steps,status,start):
    """Capture, inventory and report before cleaning up only this run; redact what leaves it."""
    from inventory import inventory, pins
    kept=status!='passed' and os.environ.get('QUIVR_KEEP_ON_FAILURE')=='1'
    # A second Ctrl+C or a CI cancellation must not abort capture, cleanup or the report.
    previous={sig:signal.signal(sig,signal.SIG_IGN) for sig in (signal.SIGINT,signal.SIGTERM)}
    try:
        try:steps.run('capture_diagnostics',stack.capture)
        except Exception:pass
        try:(stack.directory/'dependency-inventory.json').write_text(json.dumps(inventory(stack.directory/'quivr'),indent=2))
        except Exception as error:(stack.directory/'dependency-inventory.json').write_text(json.dumps({'status':'not inventoried','error':verify_report.bounded(error)}))
    finally:
        if not kept:
            try:steps.run('scoped_cleanup',stack.down,True)
            except Exception:pass
        source=run(['git','rev-parse','HEAD'],capture_output=True,text=True).stdout.strip()
        dirty=bool(run(['git','status','--porcelain','--untracked-files=no'],capture_output=True,text=True).stdout.strip())
        verify_report.write(stack.directory,{'status':status,'failed_step':steps.failed_step(),'run':stack.name,'part':getattr(stack,'part',None),'duration_seconds':round(time.monotonic()-start,3),'source':source,'dirty':dirty,
            'scope':f"Part {getattr(stack,'part',None)} of the stack verification (steps below; parts in scripts/local.py) over real PostgreSQL, Temporal, S3, Weaviate and TEI",
            'steps':steps.items,'timing_overrides':{'coverage_refresh':'100ms','delivery':DELIVERY_OVERRIDES,'change_retention_short_api':SHORT_CHANGE_RETENTION,'change_stream_poll':CHANGE_STREAM_POLL,'change_prune':PRUNE_OVERRIDES,'observability':OBSERVABILITY_OVERRIDES},'pins':pins(),
            'kept_project':stack.name if kept else None,'remaining_limits':verify_report.REMAINING_LIMITS,'artifacts':str(stack.directory),
            'preparation':preparation(stack,steps),'dependency_start_retries':getattr(stack,'readiness',{}).get('dependency_start_retries',[])})
        verify_report.redact_tree(stack.directory,verify_report.secrets_of(stack.state)+[CAPTURE_SECRET])
        report=json.loads((stack.directory/'report.json').read_text())
        # Failed tests are readable in the terminal; CI also puts them on the run page (scripts/ci_summary.py).
        print(verify_report.failure_text(report),end='',flush=True)
        print('Verification report:',stack.directory/'report.md')
        if kept:print(f'Kept for inspection (QUIVR_KEEP_ON_FAILURE=1). Remove it with: QUIVR_PROJECT={stack.name} make reset')
        for sig,handler in previous.items():signal.signal(sig,handler)

def run_stack(command,part=None):
    """dev, down, reset, migrate, or verify one part on its own isolated stack."""
    verification=command=='verify'
    # QUIVR_PROJECT selects an existing project for down/reset/migrate, e.g. a kept verification run.
    name=os.environ.get('QUIVR_PROJECT') if command in ['down','reset','migrate'] else None
    stack=Stack(name or (f'quivr-verify-{part}-'+uuid.uuid4().hex[:10] if verification else 'quivr-dev-'+__import__('hashlib').sha256(str(ROOT).encode()).hexdigest()[:10]))
    stack.part=part
    stack.verifying=verification
    def interrupted(*_):raise verify_report.Interrupted()
    signal.signal(signal.SIGTERM,interrupted)
    steps=verify_report.Steps(echo=print if verification else None);stack.steps=steps;start=time.monotonic();status='failed'
    try:
        if command in ['dev','verify']:
            # Verification starts on the template's text/markdown pin, then switches to pdf-text.
            normalizer_plugin.select(stack,'template' if verification else normalizer_plugin.from_environment())
            subscription_plugin.select(stack,verification or subscription_plugin.from_environment(),subscription_plugin.described_mode(verification))
            connector_plugin.select(stack,verification)
            # First-party connector plugins: always in verification, QUIVR_<ID>=on|off in make dev.
            connector_plugin.select_first_party(stack,[r['id'] for r in connector_plugin.FIRST_PARTY] if verification else connector_plugin.from_environment())
            steps.run('start_stack',stack.up)
            if verification:verify(stack,steps,part)
            else:print(f"API http://127.0.0.1:{stack.state['api_port']} — credentials in {stack.directory}/config.json\nUse it from this shell: eval \"$(make -s env)\"\n{normalizer_plugin.describe(stack)}\n{subscription_plugin.describe(stack)}\n{connector_plugin.describe(stack)}")
        elif command=='migrate':stack.migrate();print(f'Migrations applied to {stack.name}; restart api and worker (make dev) if the release notes require it')
        else:stack.down(command=='reset')
        status='passed'
    except (KeyboardInterrupt,verify_report.Interrupted):
        status='interrupted';raise
    finally:
        if verification:finish(stack,steps,status,start)

def environment():
    """`make env`: the export lines that point this shell at the make dev stack, as the Quickstart uses them:
    the API address, the key of Organization org_b (every Organization action, connectors included, on every
    Corpus), its webhook destination and the deployment operator's key (plugins:admin)."""
    name='quivr-dev-'+__import__('hashlib').sha256(str(ROOT).encode()).hexdigest()[:10]
    statefile=ROOT/'.scratch'/name/'state.json'
    if not statefile.exists():
        sys.exit('No make dev stack in this checkout yet: run `make dev` first.')
    s=json.loads(statefile.read_text())
    print(f"export QUIVR_API_URL=http://127.0.0.1:{s['api_port']}\nexport QUIVR_API_KEY={s['other']}\nexport QUIVR_DESTINATION=local-receiver-org-b\nexport QUIVR_OPERATOR_KEY={s['operator']}")

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('command',choices=['dev','verify','down','reset','migrate','env'])
    parser.add_argument('--part',default='',help='verify only these parts, comma-separated (default: every part, then the demo)')
    args=parser.parse_args()
    if args.command=='env':return environment()
    # Fail before building or pulling anything on a host the stack does not run on (THE-808).
    try:
        if args.command in ('dev','verify'):requirements()
    except RuntimeError as error:sys.exit(str(error))
    if args.command!='verify':return run_stack(args.command)
    if MACOS:sys.exit('make verify runs on Linux x86_64 only, as in CI; on macOS, make dev and make check work.')
    default=list(parts())+[DEMO]
    known=default+list(extra_parts())
    chosen=[p for p in args.part.replace(' ',',').split(',') if p] or default
    unknown=[p for p in chosen if p not in known]
    if unknown:parser.error(f"unknown part {', '.join(unknown)}; parts: {', '.join(known)}")
    # One part after another, each on a fresh stack; the first failed part stops the run.
    for index,part in enumerate(chosen,1):
        print(f'[verify] part {part} ({index}/{len(chosen)})',flush=True)
        if part==DEMO:run([sys.executable,'scripts/demo.py','verify'])
        else:run_stack('verify',part)
if __name__=='__main__':main()
