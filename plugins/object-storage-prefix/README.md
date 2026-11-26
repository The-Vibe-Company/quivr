# Object Storage Prefix Connector

Streams individual objects (text, XML, documents, images) under an S3-compatible prefix through Quivr attachment upload grants with resumable checkpoints.

## Modes

- `key` (default): streams objects in lexicographic key order using `ListObjectsV2` `StartAfter`. Best for append-only, backfilled, or large prefixes.
- `timestamp`: orders objects by `(LastModified, Key)` and resumes strictly after `(last_modified, last_key)` so newly uploaded and overwritten objects are picked up incrementally. Because S3 `ListObjectsV2` does not order by `LastModified`, `timestamp` mode scans the prefix into a bounded top-K snapshot (reused across pages within a run); prefer `key` mode for very large prefixes.

## Configuration

| Field | Required | Description |
| --- | --- | --- |
| `bucket` | Yes | S3-compatible bucket name. |
| `prefix` | Yes | Object key prefix (may be empty `""`). |
| `region` | Yes | S3 signing region. |
| `endpoint` | No | Optional HTTP(S) origin for an S3-compatible store (uses path-style addressing). |
| `object_patterns` | No | Slash-aware globs (`**` spans directories). Defaults to `["**"]`. |
| `exclude_patterns` | No | Slash-aware globs to skip. |
| `checkpoint_mode` | No | `key` (default) or `timestamp`. |
| `media_type` | No | Fixed media type override for every matched object. |
| `media_type_by_extension` | No | Map of lowercase file extension (for example `.xml`, `.png`) to media type. |
| `allowed_media_types` | No | Optional allowlist of resolved media types. |
| `record_key_pattern` | No | Go regexp with one capture group on the object key. Defaults to the key relative to `prefix`. |
| `source_position_pattern` | No | Go regexp with one capture group of decimal digits for revision ordering. |
| `batch_size` | No | Objects per page (`1..1000`, default `100`). |
| `concurrency` | No | Concurrent item submissions (`1..32`, default `1`). |
| `max_object_bytes` | No | Maximum object size in bytes (`1..26214400`, default `26214400`). |
| `skip_oversized` | No | Skip empty or oversized objects instead of failing (default `true`). |

## Credentials

Deposit a credential with `access_key_id`, `secret_access_key`, and optional `session_token`. Ambient AWS environment credentials are never read.

## Local certification

```bash
quivr plugin inspect plugins/object-storage-prefix
python3 scripts/plugin_object_storage_prefix_fixtures.py /tmp/prefix-fixtures
```
