"""Public-HTTP evidence while the real inference service is interrupted."""
import json, time, urllib.request, urllib.error, uuid

def verify(stack):
    s=stack.state;base=f"http://127.0.0.1:{s['api_port']}"
    def call(method,path,body=None,status=200):
        req=urllib.request.Request(base+path,method=method,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+s['admin'],'Content-Type':'application/json'})
        try:r=urllib.request.urlopen(req,timeout=8)
        except urllib.error.HTTPError as e:r=e
        with r:
            result=json.load(r)
            (stack.directory/('response-'+uuid.uuid4().hex+'.json')).write_text(json.dumps(dict(path=path,method=method,status=r.status,body=result)))
            assert r.status==status,(r.status,result)
            return result
    def await_receipt(rid,complete=False):
        deadline=time.monotonic()+60
        while True:
            r=call('GET','/v0/ingestion-receipts/'+rid)
            if r.get('availability',{}).get('searchable') and (not complete or r['processing']['state']=='idle'):return r
            assert time.monotonic()<deadline,r
            time.sleep(.2)
    cid=call('POST','/v0/corpora',{'name':'Inference recovery','idempotency_key':'inference-recovery'},201)['corpus_id']
    command={'idempotency_key':'model-before','source':{'corpus_id':cid,'namespace':'outages','record_key':'before'},'content':{'kind':'text','text':'Navigation des ferries vers la Corse.'}}
    first=await_receipt(call('POST','/v0/records',command,202)['receipt_id'],True)
    query={'query':'ferries','corpus_ids':[cid],'mode':'lexical'}
    before=call('POST','/v0/search',query)['items'][0]
    assert before['embedding_artifact_id'] and before['vector_space_id']
    stack.compose('stop','tei')
    # Replay uses the durable artifact even while inference is unavailable.
    command['idempotency_key']='model-replay'
    replay=await_receipt(call('POST','/v0/records',command,202)['receipt_id'],True)
    assert replay['version_id']==first['version_id'] and replay['outcome']=='duplicate'
    old=call('POST','/v0/search',query)['items'][0]
    assert old['embedding_artifact_id']==before['embedding_artifact_id']
    pending=[]
    for i in range(6):
        cmd={'idempotency_key':f'model-down-{i}','source':{'corpus_id':cid,'namespace':'outages','record_key':str(i)},'content':{'kind':'text','text':f'Navigation des ferries, nouvelle traversée {i}.'}}
        pending.append(call('POST','/v0/records',cmd,202)['receipt_id'])
    versions=[await_receipt(rid)['version_id'] for rid in pending]
    hits=call('POST','/v0/search',query)['items']
    assert len(hits)==7,hits
    for hit in hits:
        if hit['version_id'] in versions:assert 'embedding_artifact_id' not in hit and 'vector_space_id' not in hit
    for mode in ['semantic','hybrid']:
        query['mode']=mode
        error=call('POST','/v0/search',query,status=503)
        assert error['retryable'] and error['code']=='search_unavailable'
    stack.stop_processes();stack.compose('start','tei')
    stack.compose('up','-d','--wait','--wait-timeout','180')
    stack.config();stack.start_processes()
    for rid,vid in zip(pending,versions):assert await_receipt(rid,True)['version_id']==vid
    query['mode']='semantic';after=call('POST','/v0/search',query)['items']
    assert len(after)==7 and all(h.get('embedding_artifact_id') and h.get('vector_space_id') for h in after),after
    assert next(h for h in after if h['version_id']==first['version_id'])['embedding_artifact_id']==before['embedding_artifact_id']
    (stack.directory/'embedding-outage.json').write_text(json.dumps({'model_outage':'passed','lexical_without_vectors':6,'semantic_and_hybrid_errors':'503','worker_restart':'passed','artifact_reuse':'passed','versions_unchanged':True}))
