"""Public-HTTP evidence for scoped projection rebuilds under interruption.

The worker is stopped before acceptance, Records are corrected and withdrawn
while the Operation is queued, and inference stays stopped for the whole
rebuild. Infrastructure is only interrupted; every oracle is a public read.
"""
import json, time, urllib.request, urllib.error, uuid

def verify(stack):
    s=stack.state;base=f"http://127.0.0.1:{s['api_port']}";run=uuid.uuid4().hex[:8]
    def call(method,path,body=None,status=200,headers=False):
        req=urllib.request.Request(base+path,method=method,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+s['admin'],'Content-Type':'application/json'})
        try:r=urllib.request.urlopen(req,timeout=8)
        except urllib.error.HTTPError as e:r=e
        with r:
            result=json.load(r)
            (stack.directory/('response-'+uuid.uuid4().hex+'.json')).write_text(json.dumps(dict(path=path,method=method,status=r.status,body=result)))
            assert r.status==status,(method,path,r.status,result)
            return (result,r.headers.get('Location')) if headers else result
    def wait(what,check,timeout=90):
        deadline=time.monotonic()+timeout
        while True:
            value=check()
            if value:return value
            assert time.monotonic()<deadline,what
            time.sleep(.25)
    def receipt(rid,enriched=False):
        def ready():
            r=call('GET','/v0/ingestion-receipts/'+rid)
            if r.get('availability',{}).get('searchable') and (not enriched or r['processing']['state']=='idle'):return r
        return wait('receipt '+rid,ready)
    def ingest(cid,record,text,key=None):
        return call('POST','/v0/records',{'idempotency_key':key or record+'-'+run,'source':{'corpus_id':cid,'namespace':'rebuild','record_key':record},'content':{'kind':'text','text':text}},202)['receipt_id']
    def hits(cids,query,mode='lexical'):
        return call('POST','/v0/search',{'query':query,'corpus_ids':cids,'mode':mode})['items']
    a=call('POST','/v0/corpora',{'name':'Rebuild recovery','idempotency_key':'rebuild-recovery-'+run},201)['corpus_id']
    b=call('POST','/v0/corpora',{'name':'Rebuild neighbour','idempotency_key':'rebuild-neighbour-'+run},201)['corpus_id']
    stable=receipt(ingest(a,'brest','Le phare de Brest guide les navires.'),True)
    corrected=receipt(ingest(a,'sein','Le phare de Sein veille sur la chaussée.'),True)
    withdrawn=receipt(ingest(a,'retrait','Le phare temporaire sera retiré.'),True)
    neighbour=receipt(ingest(b,'corse','Le phare de la Corse éclaire le cap.'),True)
    prior={h['version_id']:h for h in hits([a,b],'phare')}
    assert {stable['version_id'],corrected['version_id'],withdrawn['version_id'],neighbour['version_id']}<=set(prior),prior
    assert all(h.get('embedding_artifact_id') for h in prior.values()),prior

    # Accept while no worker runs: the Operation is durable and replayable before any execution.
    stack.stop_worker()
    op,location=call('POST','/v0/corpora/'+a+'/rebuilds',{'idempotency_key':'recovery-'+run},202,headers=True)
    assert op['state']=='queued' and location=='/v0/operations/'+op['operation_id'],op
    time.sleep(1.5)
    assert call('GET',location)['state']=='queued'
    replay,again=call('POST','/v0/corpora/'+a+'/rebuilds',{'idempotency_key':'recovery-'+run},202,headers=True)
    assert replay['operation_id']==op['operation_id'] and again==location,replay
    # Mutations committed while the rebuild is pending must be reconciled before cutover.
    correction=ingest(a,'sein','Le phare de Sein restauré veille toujours.',key='sein-correction-'+run)
    call('POST','/v0/records/withdrawals',{'idempotency_key':'retrait-withdrawal-'+run,'source':{'corpus_id':a,'namespace':'rebuild','record_key':'retrait'},'reason':'rebuild scenario'},202)
    # Inference stays stopped for the whole rebuild: vectors must come from stored artifacts.
    stack.compose('stop','tei')
    stack.start_worker()
    done=wait('rebuild terminal',lambda:(lambda o:o if o['state'] in ('succeeded','failed') else None)(call('GET',location)))
    assert done['state']=='succeeded' and not done['errors'],done
    generation=done['result']['projection_generation_id']
    assert done['counters'].get('vectors_reused',0)>=1,done
    fixed=receipt(correction)
    def reconciled():
        found={h['version_id']:h for h in hits([a],'phare')}
        return found if fixed['version_id'] in found else None
    current=wait('correction searchable after cutover',reconciled)
    assert withdrawn['version_id'] not in current and corrected['version_id'] not in current,current
    assert all(h['projection_generation_id']==generation for h in current.values()),current
    assert current[stable['version_id']].get('embedding_artifact_id')==prior[stable['version_id']]['embedding_artifact_id'],current
    assert not any('retiré' in h['excerpt']['text'] for h in current.values()),current
    untouched={h['version_id']:h for h in hits([b],'phare')}
    assert untouched[neighbour['version_id']]['projection_generation_id']==prior[neighbour['version_id']]['projection_generation_id']!=generation,untouched
    assert untouched[neighbour['version_id']].get('embedding_artifact_id')==prior[neighbour['version_id']]['embedding_artifact_id']
    # Terminal replay keeps identity and target; semantic search waits for inference, then serves reused vectors.
    terminal=call('POST','/v0/corpora/'+a+'/rebuilds',{'idempotency_key':'recovery-'+run},202)
    assert terminal['operation_id']==op['operation_id'] and terminal['result']['projection_generation_id']==generation,terminal
    error=call('POST','/v0/search',{'query':'phare','corpus_ids':[a],'mode':'semantic'},503)
    assert error['retryable'] and error['code']=='model_unavailable',error
    stack.stop_processes();stack.compose('start','tei')
    stack.compose('up','-d','--wait','--wait-timeout','180')
    stack.config();stack.start_processes()
    # Drain this scenario's own enrichment backlog so later acceptance runs start unloaded.
    receipt(correction,True)
    semantic={h['version_id']:h for h in hits([a],'phare','semantic')}
    assert semantic[stable['version_id']]['embedding_artifact_id']==prior[stable['version_id']]['embedding_artifact_id'],semantic
    assert semantic[stable['version_id']]['projection_generation_id']==generation,semantic
    (stack.directory/'rebuild-recovery.json').write_text(json.dumps({'worker_stopped_before_acceptance':'passed','queued_replay':'passed','inference_stopped_rebuild':'passed','mutations_reconciled':'passed','neighbour_corpus_preserved':'passed','terminal_replay':'passed','vectors_reused':done['counters'].get('vectors_reused'),'indexed':done['counters'].get('indexed')}))
