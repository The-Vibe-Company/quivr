# SDK test ownership

- Request-token verification belongs to the normative [authentication cases](../contracts/plugins/v0/fixtures/authentication/cases.json), executed against both SDKs by [the shared conformance script](../scripts/plugin_sdk_conformance.py). Add cases there and prove each verifier guard with a mutation before removing SDK copies.
- Keep SDK tests for distinct local risks: key-ring configuration and rotation, health exemptions, handler side effects before authentication, typed inputs, output validation, transport lifecycle and public fixture helpers.
- Generator freshness belongs to the SDK check scripts. Model round trips and bundled-schema validation guard different contracts.
- SDK manifest admission covers the shape and features that SDK dispatch needs. Engine-only semantic admission remains owned by the engine; consume normative fixtures for the SDK checks that apply.
