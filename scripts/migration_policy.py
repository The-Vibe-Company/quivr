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


def index_name(parts):
    names = tuple(part['sval'] for part in parts)
    return ('public', names[0]) if len(names) == 1 else names


def index_history(sql, known):
    """Keep only explicit, unconditional nonunique index declarations.

    Renames, schema rewrites and opaque SQL discard evidence. A later CREATE
    can establish it again; an unknown index never becomes safe by its name.
    """
    known = set(known)
    try:
        for raw in parse_sql(sql):
            stmt = raw.stmt()
            kind = stmt['@']
            if kind == 'IndexStmt':
                name = (stmt['relation']['schemaname'] or 'public', stmt['idxname'])
                known.discard(name)
                if stmt['idxname'] and not any(stmt.get(flag) for flag in ('unique', 'primary', 'isconstraint', 'if_not_exists')):
                    known.add(name)
            elif kind == 'DropStmt' and stmt['removeType']['name'] == 'OBJECT_INDEX' and stmt['behavior']['name'] == 'DROP_RESTRICT':
                known.difference_update(index_name(parts) for parts in stmt['objects'])
            elif kind == 'AlterTableStmt':
                if (stmt['objtype']['name'] != 'OBJECT_TABLE'
                        or any(cmd['subtype']['name'] not in ('AT_AddColumn', 'AT_SetRelOptions', 'AT_DropNotNull') for cmd in stmt['cmds'])
                        or any(node.get('@') == 'FuncCall' for node in nodes(stmt))):
                    known.clear()
            elif kind == 'VacuumStmt' and not stmt['is_vacuumcmd']:
                # ANALYZE changes statistics, not index definitions.
                continue
            elif kind not in ('CreateStmt', 'CreateSeqStmt', 'CreateEnumStmt', 'CommentStmt'):
                if kind != 'CreateSchemaStmt' or stmt.get('schemaElts'):
                    known.clear()
    except Error:
        known.clear()
    return known


def expand_risks(sql, nonunique_indexes=frozenset()):
    """Fail closed for statements outside the small additive subset."""
    created = set()
    nonunique_indexes = set(nonunique_indexes)
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
        elif kind == 'VacuumStmt' and not stmt['is_vacuumcmd']:
            # PostgreSQL also parses ANALYZE as VacuumStmt. Statistics-only
            # analysis preserves writes and can run in the runner transaction;
            # VACUUM, including VACUUM ANALYZE, remains outside this subset.
            continue
        elif kind == 'IndexStmt':
            table = (stmt['relation']['schemaname'], stmt['relation']['relname'])
            if table not in created:
                risks.append('index on an existing table can block writes or add uniqueness')
            name = (stmt['relation']['schemaname'] or 'public', stmt['idxname'])
            nonunique_indexes.discard(name)
            if stmt['idxname'] and not any(stmt.get(flag) for flag in ('unique', 'primary', 'isconstraint', 'if_not_exists')):
                nonunique_indexes.add(name)
        elif kind == 'AlterTableStmt':
            for cmd in stmt['cmds']:
                operation = cmd['subtype']['name']
                if operation == 'AT_DropNotNull':
                    # Relaxing a constraint preserves existing rows and writes.
                    continue
                if operation == 'AT_SetRelOptions' and stmt['objtype']['name'] == 'OBJECT_TABLE':
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
        elif kind == 'DropStmt' and stmt['removeType']['name'] == 'OBJECT_INDEX':
            dropped = {index_name(parts) for parts in stmt['objects']}
            if (stmt['concurrent'] or stmt['behavior']['name'] != 'DROP_RESTRICT'
                    or any(len(parts) != 2 for parts in stmt['objects'])
                    or dropped - nonunique_indexes):
                risks.append('index drop is not a schema-qualified proven nonunique performance index with RESTRICT')
            nonunique_indexes.difference_update(dropped)
        else:
            risks.append(kind)
    return sorted(set(risks))


def policy_errors(sql, previous, nonunique_indexes=frozenset()):
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
            errors.extend('requires a separate contract release: '+risk for risk in expand_risks(sql, nonunique_indexes))
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
    base_indexes = set()
    for name in sorted(previous):
        old = subprocess.run(['git', 'show', f'{base}:migrations/{name}'], cwd=root, capture_output=True, check=True)
        sql = old.stdout.decode('utf-8')
        try:
            if sql.split('\n')[0].removesuffix('\r') != CONTRACT and not expand_risks(sql, base_indexes):
                expansions.add(name)
        except Error:
            pass
        base_indexes = set() if sql.split('\n')[0].removesuffix('\r') == CONTRACT else index_history(sql, base_indexes)
    known_indexes = set()
    for path in sorted((root / 'migrations').glob('*.sql')):
        sql = path.read_bytes().decode('utf-8')
        if path.name in inventory:
            known_indexes = index_history(sql, known_indexes)
            continue
        if path.name.endswith(('_down.sql', '.down.sql')):
            found.append(path.name+': down migrations are forbidden; roll back the application')
            continue
        found.extend(path.name+': '+error for error in policy_errors(sql, expansions, known_indexes))
        # Merged post-policy migrations are frozen too.
        if path.name in previous:
            old = subprocess.run(['git', 'show', f'{base}:migrations/{path.name}'], cwd=root, capture_output=True, check=True)
            if old.stdout != path.read_bytes():
                found.append(path.name+': merged migration is frozen')
        # Optional contracts cannot prove an index exists in an expansion.
        known_indexes = set() if sql.split('\n')[0].removesuffix('\r') == CONTRACT else index_history(sql, known_indexes)
    return found
