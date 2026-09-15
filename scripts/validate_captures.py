import json, pathlib, sys
import yaml
from jsonschema import Draft202012Validator, RefResolver
root=pathlib.Path(__file__).resolve().parents[1]
doc=yaml.safe_load((root/'contracts/http/v0/openapi.yaml').read_text())
count=0
for p in pathlib.Path(sys.argv[1]).glob('response-*.json'):
    c=json.loads(p.read_text());path=c['path'] if c['path']=='/v0/corpora' else '/v0/corpora/{corpus_id}'
    responses=doc['paths'][path][c['method'].lower()]['responses']
    schema=responses.get(str(c['status']),responses['default'])['content']['application/json']['schema']
    Draft202012Validator(schema,resolver=RefResolver.from_schema(doc)).validate(c['body']);count+=1
assert count>0,'no public response captures'
print(f'Validated {count} actual public responses')
