import { test } from 'node:test';
import assert from 'node:assert/strict';
import { exportRows } from './export.js';
test('unauthenticated export is rejected', () => assert.throws(() => exportRows(null, []), /unauthorized/));
test('tenant isolation', () => assert.deepEqual(exportRows({tenantId:'a'}, [{tenantId:'b'}]), []));
