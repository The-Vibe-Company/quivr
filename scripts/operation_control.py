"""Public-HTTP evidence for cancelling queued projection work before activation.

The worker is held stopped so the rebuild stays queued: cancellation is then
terminal at once, the dispatched workflow cannot activate it after the worker
returns, and a rerun of the canceled Operation progresses under a new linked
identity. The same holds for retrieval configuration Operations, whose
configuration becomes effective only with their activated generation. Running-state cancellation (cancel_requested -> canceled) and the
completion/cancel race are proven deterministically in adapter and Step tests.
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
    def generation(cid,version):
        found={h['version_id']:h['projection_generation_id'] for h in call('POST','/v0/search',{'query':'amer','corpus_ids':[cid],'mode':'lexical'})['items']}
        return found.get(version)
    cid=call('POST','/v0/corpora',{'name':'Operation control','idempotency_key':'operation-control-'+run},201)['corpus_id']
    rid=call('POST','/v0/records',{'idempotency_key':'amer-'+run,'source':{'corpus_id':cid,'namespace':'control','record_key':'amer'},'content':{'kind':'text','text':"L'amer du cap guide l'entrée du port."}},202)['receipt_id']
    version=wait('receipt searchable',lambda:(lambda r:r if r.get('availability',{}).get('searchable') else None)(call('GET','/v0/ingestion-receipts/'+rid)))['version_id']
    prior=wait('baseline hit',lambda:generation(cid,version))

    # Hold the worker stopped so the rebuild cannot begin.
    stack.stop_worker()
    op,location=call('POST','/v0/corpora/'+cid+'/rebuilds',{'idempotency_key':'control-'+run},202,headers=True)
    assert op['state']=='queued',op
    blocked=call('POST',location+'/rerun',{'idempotency_key':'early-'+run},409)
    assert blocked['code']=='operation_not_terminal' and not blocked['retryable'],blocked
    canceled=call('POST',location+'/cancel',{'idempotency_key':'cancel-'+run},202)
    assert canceled['operation_id']==op['operation_id'] and canceled['state']=='canceled' and 'result' not in canceled,canceled
    again=call('POST',location+'/cancel',{'idempotency_key':'cancel-again-'+run},202)
    assert again['state']=='canceled',again
    stack.start_worker()

    # A rerun of the canceled Operation is a new linked Operation that activates.
    rerun,rerun_location=call('POST',location+'/rerun',{'idempotency_key':'rerun-'+run},202,headers=True)
    assert rerun['operation_id']!=op['operation_id'] and rerun['previous_operation_id']==op['operation_id'] and rerun_location=='/v0/operations/'+rerun['operation_id'],rerun
    done=wait('rerun terminal',lambda:(lambda o:o if o['state'] in ('succeeded','failed','canceled') else None)(call('GET',rerun_location)))
    assert done['state']=='succeeded' and not done['errors'],done
    target=done['result']['projection_generation_id']
    assert target!=prior,(target,prior)
    assert wait('rerun generation served',lambda:generation(cid,version)==target),target
    # The worker processed the dispatch backlog, yet the canceled Operation never ran or activated.
    final=call('GET',location)
    assert final['state']=='canceled' and 'result' not in final and not final['errors'],final

    # Retrieval configuration: a newer accepted configuration supersedes an older
    # pending one, a canceled configuration never becomes effective and the prior
    # configuration stays queryable, and a rerun activates the configuration.
    def effective():return call('GET','/v0/corpora/'+cid)['effective_retrieval']
    prior_config=effective();served=generation(cid,version)
    mapping=lambda pointer:{'fields':[{'name':'title','source_pointer':pointer,'type':'string','roles':['search']}]}
    stack.stop_worker()
    older,older_location=call('PUT','/v0/corpora/'+cid+'/retrieval',{'idempotency_key':'config-old-'+run,'retrieval':mapping('/provenance/producer')},202,headers=True)
    newer,newer_location=call('PUT','/v0/corpora/'+cid+'/retrieval',{'idempotency_key':'config-new-'+run,'retrieval':mapping('/provenance/producer_version')},202,headers=True)
    assert older['kind']=='retrieval_configuration' and newer['state']=='queued',(older,newer)
    assert call('GET',older_location)['state']=='canceled',call('GET',older_location)
    assert call('POST',newer_location+'/cancel',{'idempotency_key':'config-cancel-'+run},202)['state']=='canceled'
    assert effective()==prior_config,effective()
    stack.start_worker()
    config_rerun,config_rerun_location=call('POST',newer_location+'/rerun',{'idempotency_key':'config-rerun-'+run},202,headers=True)
    config_done=wait('configuration rerun terminal',lambda:(lambda o:o if o['state'] in ('succeeded','failed','canceled') else None)(call('GET',config_rerun_location)))
    assert config_done['state']=='succeeded' and config_rerun['previous_operation_id']==newer['operation_id'],config_done
    assert call('GET',older_location)['state']=='canceled' and call('GET',newer_location)['state']=='canceled'
    assert effective()['fields'][0]['source_pointer']=='/provenance/producer_version',effective()
    assert wait('configured generation served',lambda:generation(cid,version)==config_done['result']['projection_generation_id']) and served!=config_done['result']['projection_generation_id']
    (stack.directory/'operation-control.json').write_text(json.dumps({'queued_cancel_before_activation':'passed','non_terminal_rerun_rejected':'passed','terminal_cancel_idempotent':'passed','rerun_of_canceled_activated':'passed','canceled_generation_never_served':'passed','newer_configuration_supersedes_pending':'passed','canceled_configuration_never_effective':'passed','configuration_rerun_activated':'passed'}))
