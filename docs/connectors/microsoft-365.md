# Microsoft 365 mailbox (`m365_mail`): operator guide

The `m365_mail` kind collects one folder of one Microsoft 365 mailbox through
Microsoft Graph, for example a shared monitoring mailbox that receives alerts,
press releases or newsletters. Read the [Connector Instances overview](README.md)
first: creation, credentials, health and the change feed work the same way for
every kind.

## What it collects

- **New mail only.** An instance collects mail received from its creation, or
  from `backfill_since` when set (at most 7 days back). Mail already in the
  folder before that point is not collected.
- **One Record per mail.** The Record Key is the mail's `internetMessageId`.
  When a mail has none, the key is `graph:` followed by its immutable Graph id.
- **Parts of each Record Version:**

  | Part key | Role | Content |
  | --- | --- | --- |
  | `title` | `title` | The subject, as text. |
  | `body` | `body` | The body as plain text. An HTML body is converted, and scripts and styles are dropped. |
  | `original_body` | `original_body` | The original HTML body, kept as a verified Blob (`text/html`). |
  | `attachment-NN` | `attachment` | One verified Blob per attachment of 25 MB or less. |

  Each attachment Part keeps the attachment's media type and its verified
  SHA-256 checksum. Its `connector.m365_mail.attachment` extension records the
  file name, size, inline flag and type (`file`, or `item` for an attached mail
  or event stored as MIME). Attachment contents are stored, not extracted:
  search covers the subject and the body only.
- **Headers.** The `connector.m365_mail` extension (schema version `1`) holds:
  - `from`, `sender`, `to`, `cc`;
  - `subject`, `sent_at`, `received_at`;
  - `conversation_id`, `internet_message_id`, `graph_id`, `folder`;
  - `to_count` and `cc_count`: the full recipient counts. `to` and `cc` keep at
    most the first 100 recipients each;
  - `attachments_skipped`: attachments that were not stored, each with a
    `reason`. The reasons are `too_large` (over 25 MB, announced or found while
    downloading), `reference_attachment`
    (a link to a cloud file, not a file) and `empty`.
- **What does not create a new Version.** Read/unread changes and flags. A
  genuine change to a mail's content or attachments becomes a correction.
- **What is never withdrawn.** Moving or deleting a mail in the mailbox leaves
  its Record untouched. Withdraw it through the ingestion API if needed.

## 1. Register an application in Microsoft Entra ID

A tenant administrator performs these steps once per organization.

1. In the Microsoft Entra admin center, open **App registrations** > **New
   registration**. Choose a descriptive name, single tenant. No redirect URI
   is needed.
2. Note the **Directory (tenant) ID** and the **Application (client) ID**.
3. Under **Certificates & secrets**, add a credential:
   - **Certificate (recommended).** Upload the public certificate. Keep the
     certificate and its RSA private key in PEM form for step 4.
   - **Client secret.** Copy the secret value when it is shown; it cannot be
     displayed again.

   Either way, note the expiry date.

## 2. Restrict the application to the monitored mailboxes

Quivr needs to read mail, including bodies and attachments. The Graph
permission for that is `Mail.Read`, which lets an application read **every**
mailbox in the tenant unless it is scoped. Scope it to the monitored
mailboxes with one of these two methods.

**Recommended: RBAC for Applications in Exchange Online.** Run these in
Exchange Online PowerShell as an Exchange administrator:

```powershell
# The enterprise application's IDs (Enterprise applications blade, not App registrations).
New-ServicePrincipal -AppId <application-client-id> -ObjectId <service-principal-object-id> -DisplayName "Quivr mail collection"
# Scope: for example, members of a mail-enabled group holding the monitored mailboxes.
New-ManagementScope -Name "Quivr monitored mailboxes" -RecipientRestrictionFilter "MemberOfGroup -eq '<group distinguished name>'"
New-ManagementRoleAssignment -App <service-principal-object-id> -Role "Application Mail.Read" -CustomResourceScope "Quivr monitored mailboxes"
# Check: InScope must be True for a monitored mailbox and False for any other.
Test-ServicePrincipalAuthorization -Identity <service-principal-object-id> -Resource monitoring@example.org
```

With this method, **do not grant `Mail.Read` to the application in Entra
ID**. Grants from Entra ID and from Exchange RBAC add up, so a tenant-wide
Entra grant would cancel the scope.

**Alternative: an application access policy.** This older mechanism, which
Microsoft is replacing with RBAC for Applications, works like this:
1. Grant the `Mail.Read` **application** permission under **API permissions**.
2. Grant admin consent.
3. Restrict it to a mail-enabled security group containing the monitored
   mailboxes:
   ```powershell
   New-ApplicationAccessPolicy -AppId <application-client-id> -PolicyScopeGroupId <group> -AccessRight RestrictAccess -Description "Quivr mail collection"
   Test-ApplicationAccessPolicy -AppId <application-client-id> -Identity monitoring@example.org
   ```

Exchange caches permission changes, so they can take from 30 minutes to 2
hours to apply. Until then Quivr may report `mailbox_access_denied`.

## 3. Deployment settings

By default the connector talks to the global Microsoft cloud. A deployment
can override both endpoints in its `QUIVR_CONFIG` file, for example for a
national cloud:

```json
"m365": { "login_endpoint": "https://login.microsoftonline.com", "graph_endpoint": "https://graph.microsoft.com/v1.0" }
```

An API caller cannot choose these endpoints, so a deposited secret is only
ever sent to them. The worker needs outbound HTTPS access to both.

## 4. Create the instance

```http
POST /v0/connectors
{
  "idempotency_key": "monitoring-mailbox-1",
  "corpus_id": "corpus_…",
  "source_namespace": "monitoring-mailbox",
  "kind": "m365_mail",
  "config": {
    "tenant_id": "00000000-0000-0000-0000-000000000000",
    "mailbox": "monitoring@example.org",
    "folder": "inbox",
    "backfill_since": "<an RFC 3339 instant within the last 7 days>"
  },
  "schedule": { "interval_seconds": 60 },
  "credential": {
    "secret": { "client_id": "11111111-1111-1111-1111-111111111111", "client_secret": "…" },
    "expires_at": "2027-01-01T00:00:00Z"
  }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `config.tenant_id` | yes | Directory (tenant) ID, or a verified domain of the tenant. |
| `config.mailbox` | yes | User principal name or object ID of the mailbox. |
| `config.folder` | no | A well-known folder name (`inbox`, the default) or a folder ID. Subfolders are not included. |
| `config.backfill_since` | no | Also collect mail received since this instant. At most 7 days before creation (a one-hour grace lets a retried creation replay), otherwise `422 invalid_config`. |
| `schedule.interval_seconds` | no | Polling interval. Defaults to 60 seconds. |
| `credential.secret` | yes | `{client_id, client_secret}` or `{client_id, certificate_pem, private_key_pem}`. Anything else is `422 invalid_credential`. |
| `credential.expires_at` | recommended | The secret's or certificate's expiry date, which enables the expiry warning. |

For a certificate, use `certificate_pem` (the `-----BEGIN CERTIFICATE-----`
block) and `private_key_pem` (an RSA key, PKCS #8 or PKCS #1). Quivr signs a
short-lived client assertion with it and never sends the key.

## 5. Rotate the secret or certificate

1. In Entra ID, add a new secret or certificate while the old one is still
   valid.
2. Deposit it:

   ```http
   PUT /v0/connectors/{connector_id}/credential
   { "idempotency_key": "rotate-2027-01", "secret": { "client_id": "…", "client_secret": "…" }, "expires_at": "2028-01-01T00:00:00Z" }
   ```

3. Once health is `active` again, delete the old credential in Entra ID.

Quivr caches access tokens in the worker's memory only, keyed by the
credential, so the next run already uses the new secret.

## Health codes and troubleshooting

`GET /v0/connectors/{connector_id}` reports health. Access problems set
`access_error` until a later successful poll. Transient problems show only in
`last_error`.

| `last_error.code` | Health | Likely cause | Fix |
| --- | --- | --- | --- |
| `credential_expiring` (state) | `credential_expiring` | `expires_at` is within the warning window (14 days by default). | Rotate the credential. |
| `credential_expired` | `access_error` | `expires_at` has passed. Quivr no longer calls Microsoft. | Rotate the credential. |
| `secret_expired` | `access_error` | Entra ID reports the client secret expired. | Create a new secret and rotate. |
| `invalid_client_credential` | `access_error` | Wrong secret, or a certificate that is not registered on the application. | Check the application's credentials and rotate. |
| `invalid_certificate` | `access_error` | The deposited PEM certificate or RSA key cannot be read. | Deposit a valid PEM pair. |
| `app_not_found` | `access_error` | Wrong client ID, or the application was deleted. | Check `client_id`. |
| `tenant_not_found` | `access_error` | Wrong `tenant_id`. | Create a new instance with the right tenant. |
| `consent_missing` | `access_error` | The permission was never granted, or consent was revoked. | Redo step 2. |
| `token_refused` | `access_error` | Any other refusal from the token endpoint. | Check the application in Entra ID. |
| `unauthorized` | `access_error` | Graph rejected the token twice in a row. | Check the application and its permissions. |
| `mailbox_access_denied` | `access_error` | The mailbox is outside the RBAC scope or access policy, or the permission was removed. | Fix the scope, then allow for the Exchange cache delay. |
| `mailbox_not_found` | `access_error` | Unknown mailbox, or a mailbox without Exchange Online. | Check `mailbox`. |
| `folder_not_found` | `access_error` | Unknown folder ID or name. | Check `folder`. |
| `throttled` | unchanged | Graph returned 429. Quivr honoured `Retry-After` and retries on a later run. | None, unless it persists. Then raise `interval_seconds`. |
| `source_unavailable`, `token_unavailable` | unchanged | Graph or the identity platform was unreachable or returned 5xx. | None; collection resumes automatically. |
| `item_rejected` | unchanged | One mail could not be ingested. Other mail keeps flowing. | Check the worker logs by connector ID. |

Other behaviour:
- **Retry-After.** Short delays (up to 10 seconds) are waited within the run.
- **Delta resync.** When Graph invalidates the folder's delta state (`410
  Gone`, `syncStateNotFound`), the connector re-reads the folder over the
  collection window, bounded to the last 7 days, without creating duplicates.
- **What is logged.** Secrets, tokens, addresses and subjects are never
  logged; logs carry the connector ID and codes only.

## Limits

- 25 MB per stored attachment. Larger attachments are listed as `too_large`.
- One folder per instance, without subfolders. Create one instance per folder,
  each with its own Source Namespace.
- A run reads up to 10 pages of 10 mails and stores up to 200 MB of
  attachments. A larger backlog continues on the next run.
- Attachment text (PDF, Office documents) is not extracted yet.

## Optional check against a real tenant

`make verify` uses a local fake of Graph (`scripts/fake_graph.py`) and never
contacts Microsoft. To check a real test tenant by hand:
1. Register an application as above, scoped to a test mailbox.
2. Run a local stack with `make dev`.
3. Create an instance with the test tenant's values.
4. Send a mail with an attachment to the test mailbox.
5. Within a minute, follow `GET /v0/changes?corpus_id=…` until the Record
   appears, then read its current Version and the attachment Blob.

Keep the tenant ID, mailbox and credential out of the repository.
