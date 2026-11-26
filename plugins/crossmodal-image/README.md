# Cross-Modal Image Enrichment and Embedding Plugin

First-party Quivr plugin (`crossmodal.image`) that enriches image Blobs and embeds full images or spatial regions into a shared Cross-modal Vector Space (`crossmodal.image.vision`) searchable via natural language.

## Contributions

- **Normalizer** (`image/png`, `image/jpeg`, `image/webp`, `image/gif`): verifies the input Blob, preserves it as an immutable Blob Part (`key: "image", role: "image"`), and records normalized image dimensions and format under the `crossmodal.image.metadata` v1 extension.
- **Ingestion** (`crossmodal.image.vision`, 512 dimensions, cosine, `indexes: [image]`, `query_modalities: [text]`): reads verified image Blob Parts by signed reference, produces full-frame or grid spatial regions with normalized `[0, 1]` bounding-box provenance (`region`), and embeds both image regions and natural-language search queries into the same vector space.

## Configuration

| Field | Default | Description |
| --- | --- | --- |
| `backend` | `builtin` | `builtin` deterministic visual/text cross-modal encoder, or `http` for an external CLIP/SigLIP-compatible embedding endpoint. |
| `base_url` | — | Required when `backend` is `http`. |
| `auth` | `none` | `none`, `bearer`, or `api-key` (reads `CROSSMODAL_API_KEY`). |
| `model` | `crossmodal/vision-text-512` | Model identifier sent to the HTTP backend and recorded in provenance. |
| `region_mode` | `full` | `full` (whole image) or `grid` (full image plus `grid_rows x grid_cols` spatial crops). |
| `grid_rows` | `2` | Grid rows (`1..4`) when `region_mode` is `grid`. |
| `grid_cols` | `2` | Grid columns (`1..4`) when `region_mode` is `grid`. |
| `max_image_bytes` | `26214400` | Maximum image size in bytes (`1024..26214400`). |
| `request_timeout_ms` | `15000` | HTTP backend request timeout in milliseconds. |

## Local certification

```bash
quivr plugin inspect plugins/crossmodal-image
quivr plugin test plugins/crossmodal-image
```
