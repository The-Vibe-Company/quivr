"""Conservative expand/contract lint using PostgreSQL's parser, never SQL regexes.

Historical SQL is hash-pinned in migrations/legacy.json. New non-additive SQL
requires a contract tag and an expansion present in the previous version.
"""
import hashlib
import json
import pathlib
import re
import subprocess

from pglast import parse_sql, Error

CONTRACT = '-- quivr:contract'
EXPANSION = re.compile(r'^-- quivr:expand ([0-9]{8}T[0-9]{4}Z_[a-z0-9_]+\.sql)$', re.M)
INVENTORY = 'migrations/legacy.json'
CONTRACT_DDL = {'CreateStmt', 'CreateSeqStmt', 'CreateEnumStmt', 'IndexStmt',
                'AlterTableStmt', 'AlterSeqStmt', 'AlterEnumStmt', 'DropStmt', 'RenameStmt', 'CommentStmt'}


def nodes(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from nodes(child)
    elif isinstance(value, (tuple, list)):
        for child in value:
            yield from nodes(child)


def runner_risks(stmt):
    found = []
    if any(node.get('concurrent') for node in nodes(stmt)):
        found.append('concurrent DDL cannot run inside the migration transaction')
    if stmt['@'] in ('CreateStmt', 'CreateSeqStmt'):
        relation = stmt.get('relation') or stmt['sequence']
        if relation['relpersistence'] != 'p' or stmt.get('oncommit', {}).get('name', 'ONCOMMIT_NOOP') != 'ONCOMMIT_NOOP':
            found.append('schema objects must be permanent without ON COMMIT actions')
    return found


def constant(expr):
    if not expr:
        return False
    if expr['@'] == 'A_Const':
        return not expr['isnull']
    if expr['@'] == 'TypeCast':
        return constant(expr['arg'])
    return False


def expand_risks(sql):
    """Fail closed for statements outside the small additive subset."""
    created = set()
    risks = []
    for raw in parse_sql(sql):
        stmt = raw.stmt()
        kind = stmt['@']
        risks.extend(runner_risks(stmt))
        if kind == 'CreateStmt':
            # LIKE, inheritance and partitions can change existing tables.
            columns = stmt.get('tableElts') or ()
            if (stmt.get('if_not_exists') or stmt.get('inhRelations') or stmt.get('partbound') or stmt.get('ofTypename')
                    or any(c['@'] not in ('ColumnDef', 'Constraint') for c in columns)):
                risks.append('CREATE TABLE with IF NOT EXISTS, inheritance, partition or LIKE')
            else:
                created.add((stmt['relation']['schemaname'], stmt['relation']['relname']))
                constraints = list(stmt.get('constraints') or ())
                for column in columns:
                    if column['@'] == 'Constraint':
                        constraints.append(column)
                    else:
                        constraints.extend(column.get('constraints') or ())
                for constraint in constraints:
                    if constraint['contype']['name'] == 'CONSTR_FOREIGN':
                        target = constraint['pktable']
                        if (target['schemaname'], target['relname']) not in created:
                            risks.append('foreign key into an existing table can constrain previous deletes')
        elif kind in ('CreateSeqStmt', 'CreateEnumStmt'):
            continue
        elif kind == 'IndexStmt':
            table = (stmt['relation']['schemaname'], stmt['relation']['relname'])
            if table not in created:
                risks.append('index on an existing table can block writes or add uniqueness')
        elif kind == 'AlterTableStmt':
            for cmd in stmt['cmds']:
                operation = cmd['subtype']['name']
                if operation == 'AT_DropNotNull':
                    # Relaxing a constraint preserves existing rows and writes.
                    continue
                if operation != 'AT_AddColumn':
                    risks.append(operation)
                    continue
                column = cmd['def_']
                constraints = column.get('constraints') or ()
                types = {c['contype']['name'] for c in constraints}
                if types - {'CONSTR_NULL', 'CONSTR_NOTNULL', 'CONSTR_DEFAULT'}:
                    risks.append('added column constrains previous writers')
                defaults = [c['raw_expr'] for c in constraints if c['contype']['name'] == 'CONSTR_DEFAULT']
                if defaults and not all(constant(expr) for expr in defaults):
                    risks.append('added column default is not a non-null constant')
                if 'CONSTR_NOTNULL' in types and not defaults:
                    risks.append('added NOT NULL column has no default for previous writers')
        else:
            risks.append(kind)
    return sorted(set(risks))


def policy_errors(sql, previous):
    errors = []
    # Match the runtime's physical LF/CRLF first line, including in files
    # read from disk: universal newline translation must not broaden the tag.
    lines = [line.removesuffix('\r') for line in sql.split('\n')]
    contract = bool(lines and lines[0] == CONTRACT)
    markers = [line for line in lines if line.startswith('-- quivr:')]
    for marker in markers:
        if marker != CONTRACT and not EXPANSION.fullmatch(marker):
            errors.append('unknown migration policy tag: '+marker)
    if CONTRACT in markers and not contract:
        errors.append('contract tag must be the first line')
    try:
        statements = parse_sql(sql)
        if not statements:
            errors.append('migration has no SQL statements')
        # Transaction control defeats the runner's atomicity/advisory lock;
        # session settings can defeat its bounded lock and statement timeouts.
        if any(s.stmt()['@'] in ('TransactionStmt', 'VariableSetStmt', 'AlterSystemStmt') for s in statements):
            errors.append('transaction control and session settings are forbidden')
        if contract:
            for raw in statements:
                stmt = raw.stmt()
                errors.extend(runner_risks(stmt))
                # Declarative DDL only: SELECT, DML, CALL and procedural bodies
                # can invoke session-setting functions indirectly. Reject calls
                # in DDL expressions too, rather than trusting their names.
                if stmt['@'] not in CONTRACT_DDL or any(node.get('@') == 'FuncCall' for node in nodes(stmt)):
                    errors.append('contracts require declarative DDL without function calls or procedural SQL')
            expands = [match.group(1) for line in lines if (match := EXPANSION.fullmatch(line))]
            if len(expands) != 1 or expands[0] not in previous:
                errors.append('contract needs exactly one -- quivr:expand <migration.sql> naming an additive expansion already in the base version')
        else:
            errors.extend('requires a separate contract release: '+risk for risk in expand_risks(sql))
            if any(EXPANSION.fullmatch(line) for line in lines):
                errors.append('expand reference is only valid on a contract migration')
    except Error as error:
        errors.append('invalid PostgreSQL syntax: '+str(error))
    return errors


def check(root, base, previous):
    inventory = json.loads((root / INVENTORY).read_text())
    found = []
    # The exception inventory itself is immutable once on main: no new bypasses.
    original = subprocess.run(['git', 'show', f'{base}:{INVENTORY}'], cwd=root, capture_output=True, text=True)
    if original.returncode == 0 and json.loads(original.stdout) != inventory:
        found.append(INVENTORY+': historical classification is frozen')
    for name, entry in inventory.items():
        path = root / 'migrations' / name
        if not path.exists() or hashlib.sha256(path.read_bytes()).hexdigest() != entry['sha256']:
            found.append(name+': historical migration was changed or removed')
    for name in previous - {path.name for path in (root / 'migrations').glob('*.sql')}:
        found.append(name+': merged migration is missing; merge main or restore it')
    expansions = set()
    for name in previous:
        old = subprocess.run(['git', 'show', f'{base}:migrations/{name}'], cwd=root, capture_output=True, check=True)
        sql = old.stdout.decode('utf-8')
        try:
            if sql.split('\n')[0].removesuffix('\r') != CONTRACT and not expand_risks(sql):
                expansions.add(name)
        except Error:
            pass
    for path in sorted((root / 'migrations').glob('*.sql')):
        if path.name in inventory:
            continue
        if path.name.endswith(('_down.sql', '.down.sql')):
            found.append(path.name+': down migrations are forbidden; roll back the application')
            continue
        found.extend(path.name+': '+error for error in policy_errors(path.read_bytes().decode('utf-8'), expansions))
        # Merged post-policy migrations are frozen too.
        if path.name in previous:
            old = subprocess.run(['git', 'show', f'{base}:migrations/{path.name}'], cwd=root, capture_output=True, check=True)
            if old.stdout != path.read_bytes():
                found.append(path.name+': merged migration is frozen')
    return found
