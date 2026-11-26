# Select the runtime role built by the default final stage:
#   core   (default) — combined image for single-machine Compose and multi-role use
#   api              — slim API image (only API Go/Python plugins + optional query encoder)
#   worker           — slim worker image (only worker Go/Python plugins; no retrieval or query encoder)
# Builders may also select --target api, --target worker, or --target core directly.
ARG QUIVR_IMAGE_ROLE=core

FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY client ./client
COPY migrations ./migrations
COPY contracts ./contracts
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/The-Vibe-Company/quivr/internal/buildinfo.Version=${VERSION} -X github.com/The-Vibe-Company/quivr/internal/buildinfo.Revision=${REVISION}" -o /quivr ./cmd/quivr

# First-party Go plugins built as a stripped (-s -w) multi-call binary with
# /usr/local/bin/quivr-<id> symlinks (core-entrypoint.py CONNECTORS). Every
# plugin module builds only on the Go SDK (make plugin-boundary), which it
# replaces with ../../sdks/go; make image-context builds both the individual
# plugin modules and the multi-call targets from exactly these COPY sources.
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS connectors
WORKDIR /src
COPY sdks/go ./sdks/go
COPY plugins ./plugins
COPY deploy/plugins ./deploy/plugins
RUN mkdir -p /out/manifests \
 && for mod in plugins/*/go.mod; do \
      [ -e "$mod" ] || continue; \
      dir=$(dirname "$mod"); id=$(basename "$dir"); \
      mkdir -p "/out/manifests/$id" && cp "$dir/quivr-plugin.yaml" "/out/manifests/$id/"; \
    done

FROM connectors AS connectors-core
RUN mkdir -p /out/bin \
 && (cd deploy/plugins && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bin/quivr-plugins ./cmd/quivr-plugins) \
 && for id in $(/out/bin/quivr-plugins --list); do \
      ln -s quivr-plugins "/out/bin/quivr-$id"; \
    done

FROM connectors AS connectors-api
RUN mkdir -p /out/bin \
 && (cd deploy/plugins && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bin/quivr-plugins ./cmd/quivr-plugins-api) \
 && for id in $(/out/bin/quivr-plugins --list); do \
      ln -s quivr-plugins "/out/bin/quivr-$id"; \
    done

FROM connectors AS connectors-worker
RUN mkdir -p /out/bin \
 && (cd deploy/plugins && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bin/quivr-plugins ./cmd/quivr-plugins-worker) \
 && for id in $(/out/bin/quivr-plugins --list); do \
      ln -s quivr-plugins "/out/bin/quivr-$id"; \
    done

# The pinned tokenizer the core.ingest plugin runs (core-entrypoint.py CONNECTORS).
FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS tokenizer
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

# Optional offline text runtime (API only). The default image contains neither
# CPU wheels nor model weights; deployment builders opt in before enabling query routing.
FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS query-encoder
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

# First-party Python sidecars: newsml-g2 always runs on worker for archive ingestion;
# alerts/pdf-text use QUIVR_DEMO_PLUGINS=1 (worker runs both, API runs alerts for previews);
# API retrieval uses QUIVR_DEMO_JEV_RERANK=1 and TYPESAFE_API_KEY (core-entrypoint.py).
# Each plugin reads the manifest next to its package, so installed plugins stay in
# editable mode at the path the final image keeps; unrun roles keep manifests only so
# api and worker still reconcile identical startup pins.
FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS plugins
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python /tmp/sdk
COPY plugins/alerts /app/plugins/alerts
COPY plugins/pdf-text /app/plugins/pdf-text
COPY plugins/newsml-g2 /app/plugins/newsml-g2
COPY plugins/jev-rerank /app/plugins/jev-rerank
RUN python -m venv /opt/quivr-plugins \
 && /opt/quivr-plugins/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt \
    /tmp/sdk -e /app/plugins/alerts -e /app/plugins/pdf-text -e /app/plugins/newsml-g2 -e /app/plugins/jev-rerank

FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS plugins-api
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python /tmp/sdk
COPY plugins/alerts /app/plugins/alerts
COPY plugins/jev-rerank /app/plugins/jev-rerank
COPY plugins/pdf-text/quivr-plugin.yaml /app/plugins/pdf-text/quivr-plugin.yaml
COPY plugins/newsml-g2/quivr-plugin.yaml /app/plugins/newsml-g2/quivr-plugin.yaml
RUN python -m venv /opt/quivr-plugins \
 && /opt/quivr-plugins/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt \
    /tmp/sdk -e /app/plugins/alerts -e /app/plugins/jev-rerank

FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS plugins-worker
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python /tmp/sdk
COPY plugins/alerts /app/plugins/alerts
COPY plugins/pdf-text /app/plugins/pdf-text
COPY plugins/newsml-g2 /app/plugins/newsml-g2
COPY plugins/jev-rerank/quivr-plugin.yaml /app/plugins/jev-rerank/quivr-plugin.yaml
RUN python -m venv /opt/quivr-plugins \
 && /opt/quivr-plugins/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt \
    /tmp/sdk -e /app/plugins/alerts -e /app/plugins/pdf-text -e /app/plugins/newsml-g2

FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS runtime-base
WORKDIR /app
COPY --from=build /quivr /usr/local/bin/quivr
COPY --from=tokenizer /app /app
COPY --from=connectors /out/manifests/ /app/plugins/
COPY deploy/railway/core-entrypoint.py /app/core-entrypoint.py
COPY third_party/french-light /usr/share/quivr/notices/french-light
USER 10001:10001
# Railway rejects a Dockerfile VOLUME; Compose mounts /tmp as tmpfs instead.
ENTRYPOINT ["python", "/app/core-entrypoint.py"]

FROM runtime-base AS api
ENV QUIVR_IMAGE_ROLE=api
COPY --from=query-encoder /opt/query-encoder /opt/query-encoder
COPY --from=query-encoder /app/scripts/prepare_query_encoder.py /app/scripts/prepare_query_encoder.py
COPY --from=query-encoder /app/third_party/query-encoder /app/third_party/query-encoder
COPY deploy/cpu /app/deploy/cpu
COPY deploy/modal/embedding_api.py /app/deploy/modal/embedding_api.py
COPY --from=plugins-api /opt/quivr-plugins /opt/quivr-plugins
COPY --from=plugins-api /app/plugins /app/plugins
COPY --from=connectors-api /out/bin/ /usr/local/bin/

FROM runtime-base AS worker
ENV QUIVR_IMAGE_ROLE=worker
COPY --from=plugins-worker /opt/quivr-plugins /opt/quivr-plugins
COPY --from=plugins-worker /app/plugins /app/plugins
COPY --from=connectors-worker /out/bin/ /usr/local/bin/

FROM runtime-base AS core
ENV QUIVR_IMAGE_ROLE=core
COPY --from=query-encoder /opt/query-encoder /opt/query-encoder
COPY --from=query-encoder /app/scripts/prepare_query_encoder.py /app/scripts/prepare_query_encoder.py
COPY --from=query-encoder /app/third_party/query-encoder /app/third_party/query-encoder
COPY deploy/cpu /app/deploy/cpu
COPY deploy/modal/embedding_api.py /app/deploy/modal/embedding_api.py
COPY --from=plugins /opt/quivr-plugins /opt/quivr-plugins
COPY --from=plugins /app/plugins /app/plugins
COPY --from=connectors-core /out/bin/ /usr/local/bin/

FROM ${QUIVR_IMAGE_ROLE} AS final
