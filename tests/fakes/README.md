# Shared provider fakes

Go plugin tests, Python certification helpers and the local acceptance stack
use the same binaries from this standalone module. Provider behavior lives
here; launchers in `process` and `scripts/fake_api.py` only build, start and
stop a process. `make check` runs this module's owners too.

Run the fake from this directory:

```sh
go run ./cmd/x
```

It binds an ephemeral loopback port and prints one JSON readiness line with
the actual `url`. `-listen 127.0.0.1:PORT` chooses a port; `-token TOKEN`
requires a specific test bearer. The default accepts nonempty test bearers,
except `x-revoked*`. No real credentials or provider calls are used.

X supports list timelines, ID lookup, list members, stream rules, webhook
registration/validation and stream links. Responses use embedded sanitized
[official examples](x/fixtures/README.md). Timeline and member page sizes are
configurable so unit goldens and acceptance scenarios retain their boundaries.

Controls are local HTTP requests:

| Endpoint | Input or result |
| --- | --- |
| `POST /_control/lists/{id}` | `posts`, `delete`, `protect`, `members`, `list_error`, `fail` |
| `POST /_control/app` | `consumer_secret`, `crc_fails`, `page_size`, `member_page_size`, `user`, `lang`, additional `media`, global `fail` |
| `POST /_control/webhooks` | `invalidate: true` marks registered webhooks invalid |
| `GET /_control/state` | Posts, rules, webhooks and links for test observations |
| `GET /_control/requests` | Provider request paths in order; drains the observation queue |

A failure takes `status`, optional absolute `reset` or relative `reset_in`,
and optional `times`; `null` clears it. Posts use provider field names and
can set `push: true` or `push: "forged"`; the control reply reports each
delivery URL and status. A forged post is delivered without entering timelines.
Registration sends a real CRC callback; linked, valid webhooks receive signed
posts matching their author rules. All callbacks and redirects stay on loopback.

Each test gets isolated state and reaps its own process. Builds are reused
within each suite, and startup waits on the readiness line rather than a sleep
or a reserved port.
