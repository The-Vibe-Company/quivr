FROM golang:1.27.1-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
COPY cmd/quivr-autoscaler ./cmd/quivr-autoscaler
COPY internal/autoscaling ./internal/autoscaling
RUN CGO_ENABLED=0 go build -trimpath -o /quivr-autoscaler ./cmd/quivr-autoscaler

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /quivr-autoscaler /usr/local/bin/quivr-autoscaler
ENTRYPOINT ["/usr/local/bin/quivr-autoscaler"]
