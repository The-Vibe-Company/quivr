# Object-storage archive connector

`connector.object_storage_archive` provides the `object_storage_archive` pull
kind. It streams `.tar.gz` and `.zip` members under an S3-compatible prefix,
then transfers their exact bytes through the engine's Upload Session grants.
The [operator guide](https://docs.quivr.thevibecompany.co/guides/archive-import)
covers configuration, revision identity, progress and restart behavior.

The source prefix is an append-only sequence of immutable archives in
lexicographic object-key order. A checkpoint pins the current key and ETag,
member offset and cumulative matching-member ordinal. Replacing or deleting an
in-progress archive stops acquisition with a source error. New archive keys
must sort after the completed key; create a separate Connector Instance to
import an earlier range.

A live gzip stream and at most 64 MiB of matching member bytes per page are
cached. A pending member can add at most 25 MiB. Eight stream slots bound the
process cache; idle entries expire after 15 minutes. Restarting discards this
cache and scans the pinned compressed archive to the checkpoint once. A cache
loss between fetch and upload rebuilds a bounded batch from the attachment
reference. No archive is extracted to disk in full. ZIP members use ranged
reads and its directory provides the matching-member count.

Identity patterns use Go regular expressions with exactly one capture group
on the URL-decoded member path. Invalid captures stop the page before it can
be checkpointed. Versions of the same Record are split into separate pages.
The default ordinal Source Position is correct only when archive order is
revision order; capture a numeric source revision when that is not true.

Build and run from this directory (the commands were tested locally):

```sh
go build -o quivr-object-storage-archive .
QUIVR_PLUGIN_PORT=9990 ./quivr-object-storage-archive
```

The packaged worker and local stacks pin this plugin automatically. An
external installation pins this manifest and its HTTP endpoint as described
in the operator guide. Ambient cloud credentials are never used: deposit
`access_key_id`, `secret_access_key` and an optional `session_token` through
the Connector Instance credential API.

Tests and certification:

```sh
go test -race ./...
```

`scripts/plugin_sdk_go.sh` certifies the plugin with synthetic S3 HTTP
fixtures; the connectors lane of `make verify` uses real SeaweedFS archives,
a pause and a plugin/worker restart. `make measure-archive` measures accepted
and searchable throughput on a running local stack; it does not run in CI.

For a running local stack, for example (replace its name):

```sh
make measure-archive args='--stack <local-stack-name> --items 1000 --batch-size 100 --concurrency 8'
```

Read the reported rates and JSON evidence path; compare concurrency settings
on the same quiet stack. The measurement creates a separate Corpus and source
bucket, disables its connector when finished, and leaves them for inspection.
Remove the local stack with its usual reset command when finished.

The engine adds per-page `acquisition` timing to connector diagnostics and
structured logs: fetch, grant, plugin upload, stored-byte verification and
durable acceptance. Worker stage times are sums across concurrent submissions;
`page_ms` is elapsed time. Run limits and the configured interval are also
reported so acquisition work can be distinguished from scheduled idle time.
A successful run that reaches its page, time or byte bound continues in a new
leased run immediately when its last page contains items, reports more and
advances the run's checkpoint. Empty pages, unchanged checkpoints, exhaustion,
errors, notices and permanent rejections retain normal interval/retry scheduling.
The `continuation` timing field reports that request; run bounds, durable
acceptance before checkpointing and same-record ordering stay in place.
See the operator guide for all fields and diagnostics-size fallback behavior.
The SDK retains up to 32 idle upload connections per storage host between
pages (128 total), with the default transport's 90-second idle expiry. Active
submissions remain bounded by `concurrency`; upload grants, checksum checks
and redirect refusal retain their existing behavior.

ZIP directories are limited to 4 MiB and 100,000 entries before metadata parsing; use tar.gz for larger member sets. A 1 MiB ranged-read window bounds ZIP buffering and avoids a network request for each deflate fragment.

Refs retain the 1,024-byte protocol bound. Escape-heavy object identities use the compact legacy recovery form if page bounds would exceed it; recovering out-of-order uploads of those refs can rescan the gzip prefix more than once.
