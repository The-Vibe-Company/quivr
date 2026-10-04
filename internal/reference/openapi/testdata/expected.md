<!-- Generated from contract/http/openapi.yaml by `make generate`. Do not edit. -->

# HTTP API reference

> Generated from `contract/http/openapi.yaml` by `make generate`. Do not edit this page: change the source and regenerate.

Notes API, version `1.2.0`.

Store and find notes.

## Authentication

Every endpoint requires `Key` unless it says otherwise.

| Scheme | Type | Description |
| --- | --- | --- |
| `Key` | HTTP `bearer` | API key. |

## Endpoints

| Endpoint | Operation | Permissions |
| --- | --- | --- |
| [`GET /v1/notes/{note_id}`](#get-v1notesnote_id) | `getNote` | `notes:read` |
| [`POST /v1/search`](#post-v1search) | `searchNotes` |  |

### Notes

#### `GET /v1/notes/{note_id}`

Operation `getNote`. Requires `notes:read`.

Read one note.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `note_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Note`](#note) | The note |
| `default` | `application/json` [`Error`](#error) | Structured error. 404 absent \| hidden. |

### Search

#### `POST /v1/search`

Operation `searchNotes`.

Find notes.

**Request body** (required): `application/json` object

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` array of [`Note`](#note)<br><br>Header `Location`: string (uri-reference). Where to read more. | Hits |

Request body `application/json` fields:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string |  |  |

## Webhooks

Requests the server sends to a receiver you run; they are not routes of this API.

### `noteChanged` (POST)

Operation `receiveNoteChanged`. No authentication.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `2XX` |  | Accepted. |

## Schemas

### `Label`

Defined in `contract/shared/common.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `text` | string |  |  |
| `next` | [`Label`](#label) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
properties:
  text:
    type: string
  next:
    $ref: '#/components/schemas/Label'
```

</details>

### `Note`

A note.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string | yes | Opaque identifier. |
| `body` | one of [`Label`](#label), string |  |  |
| `tags` | array of object |  | At most `3` items. |
| `tags[].name` | string |  | One of `red`, `blue`. |
| `meta` | map of object |  |  |
| `meta.*.score` | number |  |  |

Further rules (conditional requirements or combinations) are in the full schema below.

Example `short_note`:

```json
{
  "id": "note_1",
  "body": "Hello"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
description: A note.
additionalProperties: false
properties:
  id:
    type: string
    description: Opaque identifier.
  body:
    oneOf:
      - $ref: '#/components/schemas/Label'
      - type: string
  tags:
    type: array
    items:
      type: object
      properties:
        name:
          type: string
          enum: [red, blue]
    maxItems: 3
  meta:
    type: object
    additionalProperties:
      type: object
      properties:
        score:
          type: number
required: [id]
if:
  required: [tags]
then:
  required: [body]
```

</details>

### `Error`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
properties:
  code:
    type: string
```

</details>
