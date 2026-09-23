export function exportRows(actor, rows) {
  if (!actor) throw new Error('unauthorized');
  return rows.filter(row => row.tenantId === actor.tenantId);
}
