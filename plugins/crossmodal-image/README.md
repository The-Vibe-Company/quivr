# Cross-Modal Image Enrichment and Embedding Plugin

First-party Quivr plugin (`crossmodal.image`) that enriches image Blobs and embeds full images or spatial regions into Cross-modal Vector Spaces searchable via natural language.

## Contributions

- **Normalizer** (`image/png`, `image/jpeg`, `image/gif`): verifies the input Blob (including pixel/dimension limits before full pixel decoding), preserves it as an immutable Blob Part (`key: "image", role: "image"`), and records normalized image dimensions and format under the `crossmodal.image.metadata` v1 extension.
- **Ingestion** (512 dimensions, cosine, `indexes: [image]`, `query_modalities: [text]`):
  - `crossmodal.image.vision` (`model: crossmodal/builtin-vision-512`): served exclusively by `backend: builtin`.
  - `crossmodal.image.external` (`model: crossmodal/http-vision-512`): served exclusively by `backend: http`.

  Each space reads verified image Blob Parts by signed reference, produces full-frame or grid spatial regions with normalized `[0, 1]` bounding-box provenance (`region`), and embeds both image regions and natural-language search queries into the same vector space.

## Configuration

| Field | Default | Description |
| --- | --- | --- |
| `backend` | `builtin` | `builtin` deterministic visual/text encoder (`crossmodal.image.vision`), or `http` for an external cross-modal embedding endpoint (`crossmodal.image.external`). |
| `base_url` | — | Required when `backend` is `http`. |
| `auth` | `none` | `none`, `bearer`, or `api-key` (reads `CROSSMODAL_API_KEY`). |
| `model` | space default | Optional model identifier; must match the requested space's declared model (`crossmodal/builtin-vision-512` or `crossmodal/http-vision-512`). |
| `region_mode` | `full` | `full` (whole image) or `grid` (full image plus `grid_rows x grid_cols` spatial crops). |
| `grid_rows` | `2` | Grid rows (`1..4`) when `region_mode` is `grid`. |
| `grid_cols` | `2` | Grid columns (`1..4`) when `region_mode` is `grid`. |
| `max_image_bytes` | `26214400` | Maximum image size in bytes (`1024..26214400`). |
| `request_timeout_ms` | `4000` | Per-request HTTP backend timeout in milliseconds (`500..10000`, bounded by `query_timeout_ms: 10000` and `timeout_ms: 60000`). |

## Local certification

```bash
quivr plugin inspect plugins/crossmodal-image
quivr plugin test plugins/crossmodal-image
```
