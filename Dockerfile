FROM golang:1.22-alpine AS build

WORKDIR /workspace

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /workspace/agent-platform-runner ./cmd/agent-platform-runner

FROM alpine:3.20

WORKDIR /opt

COPY --from=build /workspace/agent-platform-runner /opt/agent-platform-runner

EXPOSE 8080

ENTRYPOINT ["/opt/agent-platform-runner"]
