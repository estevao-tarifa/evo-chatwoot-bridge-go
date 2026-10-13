# syntax=docker/dockerfile:1

# --- build ------------------------------------------------------------------
# The builder only compiles the binary; the runtime image is scratch
# (ADR-0007): static binary, non-root, no shell, no frontend assets.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /bridge \
    ./cmd/bridge

# --- runtime ----------------------------------------------------------------
FROM scratch
# CA bundle for outbound TLS to Evolution GO / Chatwoot; timezone data is
# embedded in the binary via `import _ "time/tzdata"`.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /bridge /bridge
USER 65532:65532
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/bridge", "healthcheck"]
ENTRYPOINT ["/bridge"]
