package postgres

// A null route stops every work pin of a retired registration. A route fence
// stops ingestion only when its captured owner differs from the returning owner
// of the accepted source format. Subscription evaluator pins remain unchanged.
const routingWorkStoppedSQL = `EXISTS(SELECT FROM routing_work_fences f
 JOIN pipeline_plan_roles rr ON rr.registration_id=f.registration_id AND rr.plan_id=w.plan_id
 JOIN plugin_registrations pr ON pr.id=rr.registration_id
 WHERE w.pinned_at<=f.stopped_before AND (
 f.next_routing IS NULL AND w.kind<>'subscription' OR
 w.kind='ingestion' AND (w.ingestion_registration_id IS NULL OR w.ingestion_registration_id=f.registration_id)
 AND rr.role IN ('ingestion','ingestion-default','ingestion:'||pr.plugin_id)
 AND pr.plugin_id<>COALESCE(f.next_routing->'routes'->>COALESCE(
 (SELECT NULLIF(ar.source_media_type,'') FROM ingestion_receipts rc JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot) WHERE rc.organization=w.organization AND rc.id=w.work_id),'text/plain'),f.next_routing->>'default','')))`
