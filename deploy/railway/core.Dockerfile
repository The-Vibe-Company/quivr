FROM golang:1.27.2-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY client ./client
COPY migrations ./migrations
COPY contracts ./contracts
RUN CGO_ENABLED=0 go build -trimpath -o /quivr ./cmd/quivr

# First-party Go plugins, always pinned and run beside the worker (and the API
# for the ones it calls) (core-entrypoint.py CONNECTORS): the connectors and
# the core.ingest ingestion plugin. Every plugins/<id> with a go.mod is built to
# /out/bin/quivr-<id>, and its manifest is kept at /out/plugins/<id>. A plugin
# module builds only on the Go SDK (make plugin-boundary), which it replaces
# with ../../sdks/go; make image-context builds them from exactly these COPY sources.
FROM golang:1.27.2-bookworm AS connectors
WORKDIR /src
COPY sdks/go ./sdks/go
COPY plugins ./plugins
RUN mkdir -p /out/bin /out/plugins \
 && for mod in plugins/*/go.mod; do \
      [ -e "$mod" ] || continue; \
      dir=$(dirname "$mod"); id=$(basename "$dir"); \
      (cd "$dir" && CGO_ENABLED=0 go build -trimpath -o "/out/bin/quivr-$id" .) || exit 1; \
      mkdir -p "/out/plugins/$id" && cp "$dir/quivr-plugin.yaml" "/out/plugins/$id/"; \
    done

# The pinned tokenizer the core.ingest plugin runs (core-entrypoint.py CONNECTORS).
FROM python:3.12-slim-bookworm AS tokenizer
WORKDIR /app
COPY scripts/prepare_tokenizer.py ./scripts/
COPY third_party/tokenizer ./third_party/tokenizer
COPY plugins/core-ingest/profile.json ./plugins/core-ingest/profile.json
RUN python scripts/prepare_tokenizer.py \
 && python scripts/prepare_tokenizer.py --hosted \
      --repository google/embeddinggemma-2 \
      --revision 914f7f89142e33e77833254d9c9b90c3cef7303b \
      --sha256 4d777ef5bdc1aa36227abdfb77c3e49e7b9c892d16e1b6bda41c393504828be4 \
      --output .scratch/tokenizer/embeddinggemma-2.json

# Optional offline text runtime. The default image contains neither CPU wheels
# nor model weights; deployment builders opt in before enabling query routing.
FROM python:3.12-slim-bookworm AS query-encoder
ARG QUIVR_BUILD_LOCAL_QUERY_ENCODER=0
WORKDIR /app
COPY scripts/prepare_query_encoder.py ./scripts/
COPY third_party/query-encoder ./third_party/query-encoder
RUN mkdir -p /opt/query-encoder/model /opt/query-encoder/venv \
 && case "$QUIVR_BUILD_LOCAL_QUERY_ENCODER" in \
      0) ;; \
      1) python -m venv /opt/query-encoder/venv \
         && /opt/query-encoder/venv/bin/pip install --no-cache-dir --disable-pip-version-check \
              --require-hashes -r third_party/query-encoder/requirements.txt \
         && python scripts/prepare_query_encoder.py --output /opt/query-encoder/model ;; \
      *) echo 'QUIVR_BUILD_LOCAL_QUERY_ENCODER must be 0 or 1' >&2; exit 1 ;; \
    esac

# First-party Python sidecars: newsml-g2 always runs for archive ingestion;
# alerts/pdf-text use QUIVR_DEMO_PLUGINS=1;
# API retrieval uses QUIVR_DEMO_JEV_RERANK=1 and TYPESAFE_API_KEY
# (core-entrypoint.py). Each plugin reads the manifest next to its package, so
# they are installed in editable mode at the path the final image keeps.
FROM python:3.12-slim-bookworm AS plugins
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python /tmp/sdk
COPY plugins/alerts /app/plugins/alerts
COPY plugins/pdf-text /app/plugins/pdf-text
COPY plugins/newsml-g2 /app/plugins/newsml-g2
COPY plugins/jev-rerank /app/plugins/jev-rerank
RUN python -m venv /opt/quivr-plugins \
 && /opt/quivr-plugins/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt \
    /tmp/sdk -e /app/plugins/alerts -e /app/plugins/pdf-text -e /app/plugins/newsml-g2 -e /app/plugins/jev-rerank

FROM python:3.12-slim-bookworm
WORKDIR /app
COPY --from=build /quivr /usr/local/bin/quivr
COPY --from=tokenizer /app /app
COPY --from=query-encoder /opt/query-encoder /opt/query-encoder
COPY --from=query-encoder /app/scripts/prepare_query_encoder.py /app/scripts/prepare_query_encoder.py
COPY --from=query-encoder /app/third_party/query-encoder /app/third_party/query-encoder
COPY deploy/cpu /app/deploy/cpu
COPY deploy/modal/embedding_api.py /app/deploy/modal/embedding_api.py
COPY --from=plugins /opt/quivr-plugins /opt/quivr-plugins
COPY --from=plugins /app/plugins /app/plugins
COPY --from=connectors /out/bin/ /usr/local/bin/
COPY --from=connectors /out/plugins/ /app/plugins/
COPY deploy/railway/core-entrypoint.py /app/core-entrypoint.py
COPY third_party/french-light /usr/share/quivr/notices/french-light
USER 10001:10001
ENTRYPOINT ["python", "/app/core-entrypoint.py"]
