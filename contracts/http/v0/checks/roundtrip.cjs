// Pass the generated and compiled TypeScript package directory as argv[2].
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const models = require(path.resolve(process.argv[2], 'dist/models'));
const cases = JSON.parse(fs.readFileSync(path.join(__dirname, '../examples.json'), 'utf8'));
for (const c of cases) {
  const value = models[c.schema + 'FromJSON'](c.value);
  const output = JSON.parse(JSON.stringify(models[c.schema + 'ToJSON'](value)));
  // Generated Date objects normalize zero milliseconds to .000Z. Compare the
  // known transport timestamps as instants; leave plugin JSON entirely untouched.
  const timestampPaths = {
    WebhookEvent: [['occurred_at']],
    ChangeEvent: [['occurred_at']],
    Delivery: [['event', 'occurred_at']],
  };
  for (const parts of timestampPaths[c.schema] || []) {
    const parents = parts.slice(0, -1);
    const actualParent = parents.reduce((v, key) => v[key], output);
    const expectedParent = parents.reduce((v, key) => v[key], c.value);
    const key = parts[parts.length - 1];
    const actualTime = Date.parse(actualParent[key]);
    const expectedTime = Date.parse(expectedParent[key]);
    assert.ok(Number.isFinite(actualTime) && Number.isFinite(expectedTime));
    assert.equal(actualTime, expectedTime, c.name + ': timestamp changed');
    actualParent[key] = expectedParent[key];
  }
  assert.deepEqual(output, c.value, c.name);
}
console.log(`TypeScript: ${cases.length} JSON round trips passed`);
