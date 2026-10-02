# HTTP generator templates

The strict HTTP interface and handler templates in `scripts/http-bindings/`
are adapted from [oapi-codegen v2.8.0](https://github.com/oapi-codegen/oapi-codegen/tree/v2.8.0/pkg/codegen/templates),
licensed under the Apache License, Version 2.0 (see `LICENSE.oapi-codegen`).

Quivr adds deferred request parsing and response functions to preserve existing
service authorization order, bounded reads, streaming and plugin responses.
The standard HTTP middleware template is adapted to pass original path inputs
without eager parameter parsing.
