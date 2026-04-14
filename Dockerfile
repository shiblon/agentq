# Build context is the parent directory so the local entroq replace directive works.
# docker build -f agentq/Dockerfile ..
FROM golang:1.24-bookworm AS builder

ENV CGO_ENABLED=0
WORKDIR /src

# Copy both modules so the replace directive resolves.
COPY entroq/ ./entroq/
COPY agentq/ ./agentq/

WORKDIR /src/agentq
RUN go build -o /bin/agentq ./cmd/agentq

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /bin/agentq /bin/agentq
ENTRYPOINT ["/bin/agentq"]
