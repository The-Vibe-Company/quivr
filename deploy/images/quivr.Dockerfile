FROM golang:1.27.1-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY client ./client
COPY migrations ./migrations
COPY contracts ./contracts
COPY plugins ./plugins
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/The-Vibe-Company/quivr/internal/buildinfo.Version=${VERSION} -X github.com/The-Vibe-Company/quivr/internal/buildinfo.Revision=${REVISION}" -o /out/quivr ./cmd/quivr \
 && mkdir -p /out/runtime/tmp && chmod 1777 /out/runtime/tmp \
 && for manifest in plugins/*/quivr-plugin.yaml; do \
      id=$(basename "$(dirname "$manifest")"); \
      mkdir -p "/out/plugins/$id" && cp "$manifest" "/out/plugins/$id/"; \
    done

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/The-Vibe-Company/quivr" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/quivr /usr/local/bin/quivr
COPY --from=build /out/plugins /usr/share/quivr/plugins
COPY --from=build /out/runtime/ /
ENV TMPDIR=/tmp
VOLUME ["/tmp"]
USER 10001:10001
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/quivr"]
CMD ["api"]
