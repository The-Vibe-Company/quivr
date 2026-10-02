# Graph response examples

Sanitized examples transcribed from official documentation on 2026-10-02,
not account traffic. IDs, addresses, text, dates and tokens are neutral
replacements. The shared binary embeds these templates; controls supply
scenario data, raw messages or attachment metadata when a test needs it.

- `message.json`: [get message](https://learn.microsoft.com/en-us/graph/api/message-get?view=graph-rest-1.0).
- `delta.json`: [message delta](https://learn.microsoft.com/en-us/graph/api/message-delta?view=graph-rest-1.0).
- `attachments.json`: [list attachments](https://learn.microsoft.com/en-us/graph/api/message-list-attachments?view=graph-rest-1.0).
- `token.json` and `token-error.json`: [client credentials flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow).
- `error.json`: [Graph errors](https://learn.microsoft.com/en-us/graph/errors).

This is the connector's tested subset, not the entire Graph API. Delta links
carry opaque tokens; consumers follow the full link without parsing them.
The historical golden files keep their original tokens, while comparison
normalizes only those values and replays the actual links for paging proof.
