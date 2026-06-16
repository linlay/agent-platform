FROM golang:1.25-alpine AS build

WORKDIR /workspace

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /workspace/agent-platform ./cmd/agent-platform

FROM alpine:3.20

WORKDIR /opt

COPY --from=build /workspace/agent-platform /opt/agent-platform

EXPOSE 8080

ENTRYPOINT ["/opt/agent-platform"]
