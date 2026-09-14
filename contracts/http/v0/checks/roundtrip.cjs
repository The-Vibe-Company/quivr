// Pass the generated and compiled TypeScript package directory as argv[2].
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const models = require(path.resolve(process.argv[2], 'dist/models'));
const cases = JSON.parse(fs.readFileSync(path.join(__dirname, '../examples.json'), 'utf8'));
for (const c of cases) {
  const value = models[c.schema + 'FromJSON'](c.value);
  const output = JSON.parse(JSON.stringify(models[c.schema + 'ToJSON'](value)));
  assert.deepEqual(output, c.value, c.name);
}
console.log(`TypeScript: ${cases.length} JSON round trips passed`);
