# Select go-plugin, python-plugin or core-ingest, using the release inventory.
FROM golang:1.27.1-bookworm AS go-build
WORKDIR /src
COPY sdks/go ./sdks/go
COPY plugins ./plugins
ARG PLUGIN
RUN cd "plugins/${PLUGIN}" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/plugin . \
 && cp quivr-plugin.yaml /out/quivr-plugin.yaml

FROM scratch AS go-plugin
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/The-Vibe-Company/quivr" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-build /out/plugin /usr/local/bin/plugin
COPY --from=go-build /out/quivr-plugin.yaml /app/quivr-plugin.yaml
WORKDIR /app
ENV QUIVR_PLUGIN_HOST=0.0.0.0 QUIVR_PLUGIN_PORT=8080 QUIVR_PLUGIN_MANIFEST=/app/quivr-plugin.yaml TMPDIR=/tmp
VOLUME ["/tmp"]
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/plugin"]

FROM python:3.12-slim-bookworm AS python-build
WORKDIR /src
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python ./sdks/python
COPY plugins ./plugins
ARG PLUGIN
RUN python -m venv /opt/venv \
 && /opt/venv/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt ./sdks/python "./plugins/${PLUGIN}" \
 && /opt/venv/bin/pip check \
 && mkdir /out && cp "plugins/${PLUGIN}/quivr-plugin.yaml" /out/quivr-plugin.yaml \
 && /opt/venv/bin/pip uninstall -y pip setuptools wheel

FROM python:3.12-slim-bookworm AS tokenizer
WORKDIR /app
COPY scripts/prepare_tokenizer.py ./scripts/
COPY third_party/tokenizer ./third_party/tokenizer
COPY plugins/core-ingest/profile.json ./plugins/core-ingest/profile.json
RUN python scripts/prepare_tokenizer.py \
 && .scratch/tokenizer/venv/bin/pip uninstall -y pip setuptools wheel

# The interpreter is required at runtime; package installers and headers are not.
FROM python:3.12-slim-bookworm AS python-runtime
RUN rm -rf /usr/local/lib/python3.12/site-packages/pip* \
      /usr/local/lib/python3.12/site-packages/setuptools* \
      /usr/local/lib/python3.12/site-packages/pkg_resources* \
      /usr/local/lib/python3.12/site-packages/wheel* \
      /usr/local/bin/pip* /usr/local/include /root/.cache
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/The-Vibe-Company/quivr" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /app
ENV QUIVR_PLUGIN_HOST=0.0.0.0 QUIVR_PLUGIN_PORT=8080 QUIVR_PLUGIN_MANIFEST=/app/quivr-plugin.yaml \
    PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 TMPDIR=/tmp
VOLUME ["/tmp"]
USER 10001:10001
EXPOSE 8080

FROM python-runtime AS core-ingest
COPY --from=go-build /out/plugin /usr/local/bin/plugin
COPY --from=go-build /out/quivr-plugin.yaml /app/quivr-plugin.yaml
COPY --from=tokenizer /app/.scratch/tokenizer /app/.scratch/tokenizer
ENTRYPOINT ["/usr/local/bin/plugin"]

FROM python-runtime AS python-plugin
ARG PLUGIN
ENV QUIVR_PLUGIN_MODULE=${PLUGIN}
COPY --from=python-build /opt/venv /opt/venv
COPY --from=python-build /out/quivr-plugin.yaml /app/quivr-plugin.yaml
# These plugins resolve the manifest beside the installed package. Keep the
# same bytes at both the public extraction path and that runtime lookup path.
COPY --from=python-build /out/quivr-plugin.yaml /opt/venv/lib/python3.12/site-packages/quivr-plugin.yaml
COPY deploy/images/plugin.py /app/plugin.py
ENTRYPOINT ["/opt/venv/bin/python", "/app/plugin.py"]
