# push-source

Receive versioned text records through `POST records`, secured by an instance token.
Quivr authenticates callers and returns `202` with Receipts after accepting items.
The handler receives the declared route name and parsed JSON in `ReceiveRequest`.
The scheduled fetch only reports channel health; this source makes no external calls.

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -e <quivr checkout>/sdks/python
python3 -m unittest discover -s tests
quivr plugin test --report contract-report.json
```

The fixture covers an accepted record and a refused blank record. Contract Runner
checks plugin handling; the engine owns token authentication. Send a stable `key`
and a new `revision` whenever source content changes.

See [Receive pushed records](https://docs.quivr.thevibecompany.co/plugins/push-source)
for pinning, token creation, delivery and reading the resulting Record.
