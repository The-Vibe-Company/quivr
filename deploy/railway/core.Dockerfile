FROM golang:1.27.1-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY client ./client
COPY migrations ./migrations
COPY contracts ./contracts
RUN CGO_ENABLED=0 go build -trimpath -o /quivr ./cmd/quivr

FROM python:3.12-slim-bookworm AS tokenizer
WORKDIR /app
COPY scripts/prepare_tokenizer.py scripts/token_offsets.py ./scripts/
COPY third_party/tokenizer ./third_party/tokenizer
COPY internal/processing/profile.json ./internal/processing/profile.json
RUN python scripts/prepare_tokenizer.py

# First-party plugins the worker runs as sidecars when QUIVR_DEMO_PLUGINS=1
# (core-entrypoint.py). Each plugin reads the manifest next to its package, so
# they are installed in editable mode at the path the final image keeps.
FROM python:3.12-slim-bookworm AS plugins
COPY contracts/http/v0/checks/requirements.txt /tmp/constraints.txt
COPY sdks/python /tmp/sdk
COPY plugins/alerts /app/plugins/alerts
COPY plugins/pdf-text /app/plugins/pdf-text
RUN python -m venv /opt/quivr-plugins \
 && /opt/quivr-plugins/bin/pip install --no-cache-dir --disable-pip-version-check -c /tmp/constraints.txt \
    /tmp/sdk -e /app/plugins/alerts -e /app/plugins/pdf-text

FROM python:3.12-slim-bookworm
WORKDIR /app
COPY --from=build /quivr /usr/local/bin/quivr
COPY --from=tokenizer /app /app
COPY --from=plugins /opt/quivr-plugins /opt/quivr-plugins
COPY --from=plugins /app/plugins /app/plugins
COPY deploy/railway/core-entrypoint.py /app/core-entrypoint.py
USER 10001:10001
ENTRYPOINT ["python", "/app/core-entrypoint.py"]
