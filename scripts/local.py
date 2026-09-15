#!/usr/bin/env python3
"""Linux local Corpus slice: host Go processes and an isolated Compose PostgreSQL."""
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
        self.statefile=self.directory/'state.json'
        if self.statefile.exists(): self.state=json.loads(self.statefile.read_text())
        else:
            self.state={'password':secrets.token_hex(24),'cursor_key':secrets.token_hex(32), 'admin':secrets.token_hex(32),'other':secrets.token_hex(32),'reader':secrets.token_hex(32),'scoped':secrets.token_hex(32),'denied':secrets.token_hex(32),'pids':[], 'api_port':port(),'probe_port':port(),'worker_probe_port':port()}
            self.save()
    def save(self):
        self.statefile.write_text(json.dumps(self.state));self.statefile.chmod(0o600)
    def compose(self,*args,**kwargs):
        return run(['docker','compose','-p',self.name,'-f','deploy/compose/compose.yaml',*args],env={**os.environ,'QUIVR_DB_PASSWORD':self.state['password']},**kwargs)
    def config(self):
        address=self.compose('port','postgres','5432',capture_output=True,text=True).stdout.strip()
        s=self.state
        scope=lambda org,actions,corpora:dict(organization=org,actions=actions,corpora=corpora)
        cfg=dict(log_directory=str(self.directory),database_url=f"postgres://quivr:{s['password']}@{address}/quivr?sslmode=disable",listen=f"127.0.0.1:{s['api_port']}",probe_listen=f"127.0.0.1:{s['probe_port']}",cursor_key=s['cursor_key'],keys={
            s['admin']:scope('org_a',['corpora:read','corpora:write'],['*']),
            s['other']:scope('org_b',['corpora:read','corpora:write'],['*']),
            s['reader']:scope('org_a',['corpora:read'],['*']),
            s['scoped']:scope('org_a',['corpora:read','corpora:write'],[s.get('scoped_id','corpus_not_granted')]),
            s['denied']:scope('org_a',['content:read'],['*'])})
        f=self.directory/'config.json';f.write_text(json.dumps(cfg));f.chmod(0o600)
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
        run([GO,'build','-o',str(self.directory/'quivr'),'./cmd/quivr'])
        self.compose('up','-d','--wait','--wait-timeout','60')
        self.migrate();self.migrate();self.start_processes()
    def tests(self,pattern):
        s=self.state
        env={**os.environ,'QUIVR_TEST_CAPTURES':str(self.directory),'QUIVR_TEST_URL':f"http://127.0.0.1:{s['api_port']}",**{'QUIVR_TEST_'+k.upper():s[k] for k in ['admin','other','reader','scoped','denied']}}
        with (self.directory/'acceptance.log').open('a') as log:
            result=subprocess.run([GO,'test','-count=1','-v','-run',pattern,'./tests/acceptance'],cwd=ROOT,env=env,stdout=log,stderr=subprocess.STDOUT)
        if result.returncode:raise RuntimeError('acceptance failed; inspect '+str(self.directory/'acceptance.log'))
    def capture(self):
        with (self.directory/'postgres.log').open('w') as log:self.compose('logs','--no-color','postgres',stdout=log,stderr=log)
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
                stack.stop_processes();stack.compose('restart','postgres');stack.compose('up','-d','--wait','--wait-timeout','60');stack.migrate();stack.start_processes()
                req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/corpora/{original_id}",headers={'Authorization':'Bearer '+stack.state['admin']})
                with urllib.request.urlopen(req,timeout=5) as r: assert json.load(r)['corpus_id']==original_id
                stack.tests('TestCorpusPersistsAndReplays')
                req=urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/corpora",headers={'Authorization':'Bearer '+stack.state['admin']})
                with urllib.request.urlopen(req,timeout=5) as r:stack.state['scoped_id']=json.load(r)['items'][0]['corpus_id']
                stack.save();stack.stop_processes();stack.config();stack.start_processes();stack.tests('TestAuthorization|TestValidation|TestPagination|TestConcurrent')
                run([os.environ.get('CONTRACT_PYTHON',str(ROOT/'.scratch/contracts/venv/bin/python')),'scripts/validate_captures.py',str(stack.directory)])
            else:print(f"API http://127.0.0.1:{stack.state['api_port']} — credentials in {stack.directory}/config.json")
        elif args.command=='migrate':stack.migrate()
        else:stack.down(args.command=='reset')
        status='passed'
    finally:
        if verification:
            try:stack.capture()
            finally:stack.down(True)
            (stack.directory/'report.json').write_text(json.dumps({'status':status,'duration_seconds':round(time.monotonic()-start,3),'source':run(['git','rev-parse','HEAD'],capture_output=True,text=True).stdout.strip(),'scope':'Corpus HTTP acceptance; local Linux processes + real PostgreSQL','artifacts':str(stack.directory)},indent=2))
            print('Verification artifacts:',stack.directory)
if __name__=='__main__':main()
