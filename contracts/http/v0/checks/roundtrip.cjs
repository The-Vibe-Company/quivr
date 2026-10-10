// Pass the generated and compiled TypeScript package directory as argv[2].
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const models = require(path.resolve(process.argv[2], 'dist/models'));
const cases = JSON.parse(fs.readFileSync(path.join(__dirname, '../examples.json'), 'utf8'));
for (const c of cases) {
  // The generator renames Error to avoid JavaScript's built-in Error class.
  const name = c.schema === 'Error' ? 'ModelError' : c.schema;
  const value = models[name + 'FromJSON'](c.value);
  const output = JSON.parse(JSON.stringify(models[name + 'ToJSON'](value)));
  // Generated Date objects normalize zero milliseconds to .000Z. Compare the
  // known transport timestamps as instants; leave plugin JSON entirely untouched.
  const timestampPaths = {
    WebhookEvent: [['occurred_at']],
    ChangeEvent: [['occurred_at']],
    Version: [['accepted_at']],
    Delivery: [['event', 'occurred_at']],
    ConnectorCreate: [['credential', 'expires_at']],
    ConnectorToken: [['created_at'], ['rotated_at'], ['revoked_at'], ['valid_until']],
    ConnectorTokenCreated: [['token', 'created_at'], ['token', 'rotated_at'], ['token', 'revoked_at'], ['token', 'valid_until']],
    ConnectorTokenList: [['items', '*', 'created_at'], ['items', '*', 'rotated_at'], ['items', '*', 'revoked_at'], ['items', '*', 'valid_until']],
    Connector: [['created_at'], ['credential', 'deposited_at'], ['credential', 'expires_at'], ['health', 'evaluated_at'],
      ['health', 'last_success_at'], ['health', 'last_item_at'], ['health', 'last_error', 'at']],
    PipelinePlan: [['created_at'], ['activated_at']],
    // '*' stands for every index of an array.
    PluginRegistrationList: [['items', '*', 'created_at'], ['items', '*', 'updated_at']],
    PluginRegistration: [['created_at'], ['updated_at'], ['check', 'checked_at']],
    DocumentTimeline: [['steps', '*', 'at'], ...['accepted_at', 'materialized_at', 'segmented_at', 'retrieval_ready_at', 'enriched_at']
      .map((step) => ['document', 'steps', step])],
    PluginCallStatsList: [['from'], ['to'], ['items', '*', 'summary', 'last_error_at'], ['items', '*', 'points', '*', 'start']],
    ReceivedStatsList: [['from'], ['to'], ['items', '*', 'points', '*', 'start']],
    MatchStatsList: [['from'], ['to'], ['items', '*', 'points', '*', 'start']],
    TopQueryList: [['items', '*', 'points', '*', 'start']],
  };
  const expand = (parts) => {
    const star = parts.indexOf('*');
    if (star < 0) return [parts];
    const items = parts.slice(0, star).reduce((v, key) => v[key], c.value);
    return items.flatMap((_, i) => expand([...parts.slice(0, star), i, ...parts.slice(star + 1)]));
  };
  for (const parts of (timestampPaths[c.schema] || []).flatMap(expand)) {
    const parents = parts.slice(0, -1);
    const actualParent = parents.reduce((v, key) => v[key], output);
    const expectedParent = parents.reduce((v, key) => v[key], c.value);
    const key = parts[parts.length - 1];
    // An optional timestamp an example leaves out stays out.
    if (!(key in expectedParent)) continue;
    const actualTime = Date.parse(actualParent[key]);
    const expectedTime = Date.parse(expectedParent[key]);
    assert.ok(Number.isFinite(actualTime) && Number.isFinite(expectedTime));
    assert.equal(actualTime, expectedTime, c.name + ': timestamp changed');
    actualParent[key] = expectedParent[key];
  }
  assert.deepEqual(output, c.value, c.name);
}
console.log(`TypeScript: ${cases.length} JSON round trips passed`);
