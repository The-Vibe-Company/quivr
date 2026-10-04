import json, pathlib, sys
from api_contract import check_response

count = 0
for p in pathlib.Path(sys.argv[1]).glob('response-*.json'):
    capture = json.loads(p.read_text())
    check_response(capture['method'], capture['path'], capture['status'], capture['body'])
    count += 1
assert count > 0, 'no public response captures'
print(f'Validated {count} actual public responses')
