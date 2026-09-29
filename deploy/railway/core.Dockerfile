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

FROM python:3.12-slim-bookworm
WORKDIR /app
COPY --from=build /quivr /usr/local/bin/quivr
COPY --from=tokenizer /app /app
COPY deploy/railway/core-entrypoint.py /app/core-entrypoint.py
USER 10001:10001
ENTRYPOINT ["python", "/app/core-entrypoint.py"]
