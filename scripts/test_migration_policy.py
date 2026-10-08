"""Owner tests for SQL compatibility lint: real PostgreSQL syntax, no SQL mocks."""
import hashlib
import json
import pathlib
import subprocess
import tempfile
import unittest
import migration_policy as p


class PolicyTests(unittest.TestCase):
    def test_expand_accepts_additions_and_rejects_write_incompatibility(self):
        cases = [
            ("CREATE TABLE items(id int PRIMARY KEY); CREATE INDEX by_id ON items(id);", True),
            ("ALTER TABLE items ADD COLUMN optional text;", True),
            ("CREATE TABLE parent(id int PRIMARY KEY); CREATE TABLE child(id int REFERENCES parent(id));", True),
            ("CREATE TABLE self_ref(id int PRIMARY KEY, parent int REFERENCES self_ref(id));", True),
            ("CREATE TABLE child(id int REFERENCES old_parent(id));", False),
            ("CREATE TABLE child(parent int, FOREIGN KEY(parent) REFERENCES old_parent(id));", False),
            ("ALTER TABLE items ADD COLUMN state text NOT NULL DEFAULT 'queued';", True),
            ("ALTER TABLE items ADD COLUMN flags jsonb DEFAULT '{}'::jsonb;", True),
            ("ALTER TABLE items SET (vacuum_truncate=false, fillfactor=70, autovacuum_vacuum_threshold=10);", True),
            ("ALTER TABLE items SET (toast.autovacuum_vacuum_scale_factor=0);", True),
            ("ALTER INDEX by_id SET (fillfactor=70);", False),
            ("ALTER TABLE items RESET (fillfactor);", False),
            ("ALTER TABLE items SET SCHEMA other;", False),
            ("ALTER TABLE items SET (fillfactor=70), DROP COLUMN title;", False),
            ("-- DROP TABLE is just a comment\nALTER TABLE items ADD COLUMN note text DEFAULT 'DROP TABLE';", True),
            ("ALTER TABLE items DROP COLUMN title;", False),
            ("ALTER TABLE items RENAME COLUMN title TO name;", False),
            ("ALTER TABLE items ALTER COLUMN title TYPE bigint;", False),
            ("ALTER TABLE items ADD COLUMN required text NOT NULL;", False),
            ("ALTER TABLE items ADD COLUMN id int UNIQUE;", False),
            ("ALTER TABLE items ADD COLUMN ref int REFERENCES other(id);", False),
            ("ALTER TABLE items ADD COLUMN number int DEFAULT random();", False),
            ("ALTER TABLE items ADD COLUMN x int, DROP COLUMN title;", False),
            ("WITH removed AS (DELETE FROM items RETURNING *) SELECT * FROM removed;", False),
            ("DO $$ BEGIN EXECUTE 'DROP TABLE items'; END $$;", False),
            ("CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;", False),
            ("CREATE UNIQUE INDEX singleton ON items(id);", False),
            ("CREATE INDEX by_id ON items(id);", False),
            ("CREATE TABLE items (LIKE old INCLUDING ALL);", False),
            ("CREATE TABLE IF NOT EXISTS items(id int); CREATE UNIQUE INDEX one ON items(id);", False),
            ("CREATE TEMP TABLE items(id int);", False),
            ("CREATE SEQUENCE items_seq;", True),
            ("CREATE TEMP SEQUENCE items_seq;", False),
            ("CREATE UNLOGGED SEQUENCE items_seq;", False),
            ("CREATE UNLOGGED TABLE items(id int);", False),
            ("CREATE TABLE items(id int) ON COMMIT DROP;", False),
            ("CREATE TABLE items(id int); CREATE INDEX CONCURRENTLY by_id ON items(id);", False),
            ("BEGIN; DROP TABLE items; COMMIT;", False),
        ]
        for sql, allowed in cases:
            with self.subTest(sql=sql):
                self.assertEqual(not p.expand_risks(sql), allowed)

    def test_expand_drops_only_proven_nonunique_performance_indexes(self):
        # The guard's public repository boundary must resolve earlier index
        # declarations, rather than trusting a name or accepting every DROP.
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            def git(*args):
                subprocess.run(['git', *args], cwd=root, check=True, capture_output=True)
            git('init', '-q', '-b', 'main')
            git('config', 'user.email', 'test@example.invalid')
            git('config', 'user.name', 'test')
            (root / 'migrations').mkdir()
            baseline = root / 'migrations/20261001T0000Z_indexes.sql'
            baseline.write_text('''CREATE TABLE items(id int PRIMARY KEY);
CREATE INDEX replaced ON items(id);
DROP INDEX replaced;
CREATE UNIQUE INDEX replaced ON items(id);
CREATE UNIQUE INDEX ambiguous ON items(id);
CREATE INDEX IF NOT EXISTS ambiguous ON items(id);
CREATE INDEX renamed ON items(id);
ALTER INDEX renamed RENAME TO former;
CREATE UNIQUE INDEX identity ON items(id);
CREATE INDEX by_id ON items(id);
CREATE SCHEMA other;
CREATE TABLE other.items(id int);
CREATE INDEX by_id ON other.items(id);''')
            (root / p.INVENTORY).write_text(json.dumps({baseline.name: {
                'sha256': hashlib.sha256(baseline.read_bytes()).hexdigest(),
                'classification': 'legacy-risk', 'risks': ['RenameStmt']}}))
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'base')
            candidate = root / 'migrations/20261002T0000Z_tuning.sql'
            cases = [
                ('ALTER TABLE items SET (vacuum_truncate=false); DROP INDEX by_id;', True),
                ('DROP INDEX public.by_id;', True),
                ('DROP INDEX other.by_id;', True),
                ('DROP INDEX IF EXISTS public.by_id;', True),
                ('DROP INDEX identity;', False),
                ('DROP INDEX items_pkey;', False),
                ('DROP INDEX replaced;', False),
                ('DROP INDEX ambiguous;', False),
                ('DROP INDEX renamed;', False),
                ('DROP INDEX former;', False),
                ('DROP INDEX missing;', False),
                ('DROP INDEX absent.by_id;', False),
                ('DROP INDEX by_id, identity;', False),
                ('DROP INDEX by_id; CREATE TABLE extra(id int); CREATE UNIQUE INDEX by_id ON extra(id); DROP INDEX by_id;', False),
                ('DROP INDEX by_id CASCADE;', False),
                ('DROP INDEX CONCURRENTLY by_id;', False),
                ('DROP TABLE items;', False),
            ]
            for sql, allowed in cases:
                with self.subTest(sql=sql):
                    candidate.write_text(sql)
                    errors = p.check(root, 'main', {baseline.name})
                    self.assertEqual(not errors, allowed, errors)

    def test_contract_requires_expansion_already_in_previous_version(self):
        old = '20261001T0000Z_expand.sql'
        self.assertEqual(p.policy_errors('-- quivr:contract\n-- quivr:expand '+old+'\nDROP TABLE items;', {old}), [])
        self.assertEqual(p.policy_errors('-- quivr:contract\n-- quivr:expand '+old+'\nCREATE SEQUENCE items_seq;', {old}), [])
        self.assertEqual(p.policy_errors('-- quivr:contract\r\n-- quivr:expand '+old+'\r\nDROP TABLE items;', {old}), [])
        for ending in ['\v', '\f', '\u2028']:
            self.assertTrue(p.policy_errors('-- quivr:contract'+ending+'\n-- quivr:expand '+old+'\nDROP TABLE items;', {old}))
        for sql in [
            '-- quivr:contract\nDROP TABLE items;',
            '-- quivr:contract\n-- quivr:expand 20261002T0000Z_unreleased.sql\nDROP TABLE items;',
            '-- intro\n-- quivr:contract\nDROP TABLE items;',
            '-- quivr:contrcat\nDROP TABLE items;',
            '-- quivr:contract\n-- quivr:expand '+old+'\nCOMMIT; DROP TABLE items;',
            '-- quivr:contract\n-- quivr:expand '+old+"\nSELECT set_config('statement_timeout','0',true);",
            '-- quivr:contract\n-- quivr:expand '+old+"\nDO $$ BEGIN PERFORM set_config('lock_timeout','0',true); END $$;",
            '-- quivr:contract\n-- quivr:expand '+old+"\nALTER TABLE items ADD COLUMN x text DEFAULT set_config('lock_timeout','0',true);",
            '-- quivr:contract\n-- quivr:expand '+old+'\nDROP INDEX CONCURRENTLY by_id;',
            '-- quivr:contract\n-- quivr:expand '+old+'\nCREATE TEMP TABLE items(id int);',
            '-- quivr:contract\n-- quivr:expand '+old+'\nCREATE TEMP SEQUENCE items_seq;',
            '-- quivr:contract\n-- quivr:expand '+old+'\nCREATE UNLOGGED SEQUENCE items_seq;',
        ]:
            with self.subTest(sql=sql):
                self.assertTrue(p.policy_errors(sql, {old}))


    def test_history_cannot_be_edited_or_added_to_exception_inventory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            def git(*args):
                subprocess.run(['git', *args], cwd=root, check=True, capture_output=True)
            git('init', '-q', '-b', 'main')
            git('config', 'user.email', 'test@example.invalid')
            git('config', 'user.name', 'test')
            (root / 'migrations').mkdir()
            path = root / 'migrations/20261001T0000Z_a.sql'
            original = b'CREATE TABLE items(id int);'
            path.write_bytes(original)
            entry = {'sha256': hashlib.sha256(original).hexdigest(), 'classification': 'additive', 'risks': []}
            legacy_risk = root / 'migrations/20261001T0001Z_risk.sql'
            legacy_risk.write_text('DROP TABLE old_items;')
            inventory = root / p.INVENTORY
            baseline = json.dumps({path.name: entry, legacy_risk.name: {
                'sha256': hashlib.sha256(legacy_risk.read_bytes()).hexdigest(),
                'classification': 'legacy-risk', 'risks': ['DropStmt']}})
            inventory.write_text(baseline)
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'base')
            self.assertEqual(p.check(root, 'main', {path.name}), [])
            contract = root / 'migrations/20261002T0001Z_contract.sql'
            contract.write_bytes(('-- quivr:contract\r\n-- quivr:expand '+path.name+'\r\nDROP TABLE items;').encode())
            self.assertEqual(p.check(root, 'main', {path.name}), [])
            for separator in ['\v', '\f', '\u2028', '\r']:
                contract.write_bytes(('-- quivr:contract'+separator+'-- other text\n-- quivr:expand '+path.name+'\nDROP TABLE items;').encode())
                self.assertTrue(p.check(root, 'main', {path.name}))
            contract.unlink()
            path.write_text('DROP TABLE items;')
            self.assertTrue(any('changed or removed' in e for e in p.check(root, 'main', {path.name})))
            path.write_bytes(original)
            unsafe = root / 'migrations/20261002T0000Z_unsafe.sql'
            unsafe.write_text('DROP TABLE items;')
            inventory.write_text(json.dumps({path.name: entry, unsafe.name: {
                'sha256': hashlib.sha256(unsafe.read_bytes()).hexdigest(),
                'classification': 'legacy-risk', 'risks': ['DropStmt']}}))
            self.assertTrue(any('classification is frozen' in e for e in p.check(root, 'main', {path.name})))
            inventory.write_text(baseline)
            unsafe.unlink()
            down = root / 'migrations/20261002T0000Z_down.sql'
            down.write_text('DROP TABLE items;')
            self.assertEqual(p.check(root, 'main', {path.name}), [down.name+': down migrations are forbidden; roll back the application'])
            down.unlink()
            path.unlink()
            self.assertTrue(any('merged migration is missing' in e for e in p.check(root, 'main', {path.name})))
            path.write_bytes(original)
            expansion = root / 'migrations/20261002T0000Z_expand.sql'
            expansion.write_text('ALTER TABLE items ADD COLUMN note text;')
            contract.write_text('-- quivr:contract\n-- quivr:expand '+path.name+'\nDROP TABLE items;')
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'later release')
            previous = {path.name, expansion.name, contract.name, legacy_risk.name}
            self.assertEqual(p.check(root, 'main', previous), [])
            expansion.write_text('ALTER TABLE items ADD COLUMN changed text;')
            self.assertTrue(any('merged migration is frozen' in e for e in p.check(root, 'main', previous)))
            expansion.write_text('ALTER TABLE items ADD COLUMN note text;')
            pending = root / 'migrations/20261003T0000Z_cleanup.sql'
            for reference in (contract.name, legacy_risk.name):
                pending.write_text('-- quivr:contract\n-- quivr:expand '+reference+'\nDROP TABLE items;')
                self.assertTrue(any('additive expansion' in e for e in p.check(root, 'main', previous)))
