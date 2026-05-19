FROM golang:1.26-bookworm AS builder
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /bin/agentq ./cmd/agentq

FROM gcr.io/distroless/static-debian12
COPY --from=builder /bin/agentq /bin/agentq
ENTRYPOINT ["/bin/agentq"]
