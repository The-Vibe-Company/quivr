// Interoperability vector, not a production receiver implementation.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const v = JSON.parse(fs.readFileSync(path.join(__dirname, '../webhook-vector.json'), 'utf8'));
const secret = Buffer.from(v.secret.slice('whsec_'.length), 'base64');
function sign(id, timestamp, body) {
  return 'v1,' + crypto.createHmac('sha256', secret)
    .update(id + '.' + timestamp + '.' + body, 'utf8').digest('base64');
}
assert.equal(secret.length, 32);
assert.equal(sign(v.event_id, v.timestamp, v.body), v.signature);
assert.equal(JSON.parse(v.body).event_id, v.event_id);
assert.notEqual(sign(v.event_id, v.timestamp, v.body + ' '), v.signature);
assert.notEqual(sign(v.event_id + '_changed', v.timestamp, v.body), v.signature);
assert.notEqual(sign(v.event_id, String(Number(v.timestamp) + 1), v.body), v.signature);
console.log('Webhook: raw-body signature matches; body/id/timestamp tampering changes signature');
