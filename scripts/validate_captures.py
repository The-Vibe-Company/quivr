import json, pathlib, sys
from jsonschema import Draft202012Validator, RefResolver
root=pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0,str(root/'contracts/http/v0'))
from bundle import load
doc=load()  # openapi.yaml with the shared Manifest schema inlined
count=0
for p in pathlib.Path(sys.argv[1]).glob('response-*.json'):
    c=json.loads(p.read_text());path=c['path']
    if path not in doc['paths']:
        import re
        matches=[pattern for pattern in doc['paths'] if re.fullmatch(re.sub(r'\{[^}]+\}',r'[^/]+',pattern),path)]
        assert len(matches)==1,(path,matches)
        path=matches[0]
    responses=doc['paths'][path][c['method'].lower()]['responses']
    schema=responses.get(str(c['status']),responses['default'])['content']['application/json']['schema']
    Draft202012Validator(schema,resolver=RefResolver.from_schema(doc)).validate(c['body']);count+=1
assert count>0,'no public response captures'
print(f'Validated {count} actual public responses')
