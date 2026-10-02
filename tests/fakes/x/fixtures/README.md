# X response examples

Sanitized examples transcribed from official documentation on 2026-10-02,
not traffic captured from an account. IDs, text, users, dates and pagination
tokens are neutral replacements. The binary embeds these response templates;
controls replace their scenario data, rather than maintaining language-specific
provider responses.

- `timeline.json`: [list timeline quickstart](https://docs.x.com/x-api/lists/list-tweets/quickstart).
- `lookup.json`: [post lookup quickstart](https://docs.x.com/x-api/posts/lookup/quickstart)
  and the partial-error shape of [the ID lookup reference](https://docs.x.com/x-api/posts/get-posts-by-ids).
- `delivery.json`: [filtered stream webhook quickstart](https://docs.x.com/x-api/webhooks/stream/quickstart).
  The [introduction](https://docs.x.com/x-api/webhooks/stream/introduction) describes edit-history ordering.

CRC and signatures follow the [webhook documentation](https://docs.x.com/x-api/webhooks/introduction).
Only loopback callbacks are allowed. This is the subset exercised by connector
tests, not a complete implementation of the X API.
