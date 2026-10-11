# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/Syt100/mailpulse/internal/transport/cli.Version=$VERSION" -o /out/mailpulse ./cmd/mailpulse

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates && addgroup -g 10001 mailpulse && adduser -D -H -u 10001 -G mailpulse mailpulse && mkdir /data && chown 10001:10001 /data
COPY --from=build /out/mailpulse /usr/local/bin/mailpulse
LABEL org.opencontainers.image.title="MailPulse" \
      org.opencontainers.image.description="Self-hosted Gmail, QQ and personal Outlook notifications via ntfy" \
      org.opencontainers.image.source="https://github.com/Syt100/mailpulse"
ENV MAILPULSE_DATA_DIR=/data MAILPULSE_KEY_FILE=/run/secrets/mailpulse_key
USER 10001:10001
WORKDIR /data
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 CMD ["mailpulse", "healthcheck"]
ENTRYPOINT ["mailpulse"]
CMD ["serve"]

