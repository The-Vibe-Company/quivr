# Develop __PLUGIN_ID__

This connector returns the items in its instance configuration, one page at a
time. Its checkpoint is the offset of the next item. Replace the static lookup
with your source's API calls when building your connector.

## Prerequisites

Use Python 3.12 or later and a Quivr checkout containing connector SDK support.
From this plugin directory, install the SDK in a virtual environment:

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install -e <path-to-quivr-checkout>/sdks/python
```

## Steps

```bash
quivr plugin inspect .
python3 -m unittest discover -s tests
quivr plugin test --report contract-report.json .
```

## Check it worked

The sample fixture returns five items in pages of two, two and one. The revoked
fixture checks that both fetch and credential verification report an access
error. Certification succeeds only when all fixtures pass.

Implement `fetch` and `check_credential` in `__PLUGIN_MODULE__/connector.py`.
Read secrets through `request.credential["token"]`; its representation and
`request.to_dict()` are redacted. Use `request.logger` for redacted request logs.
Raise `AccessError` for rejected credentials,
`TransientError` for retryable failures, and `SourceError` for source failures.
Raise `NotDue` when no fetch is due; a successful empty page means the source
was checked and had nothing new.
