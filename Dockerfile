FROM node:24-alpine AS web-builder

WORKDIR /src/internal/entry/web/ui

COPY internal/entry/web/ui/package.json internal/entry/web/ui/package-lock.json ./
RUN npm ci

COPY internal/entry/web/ui ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25 AS builder

WORKDIR /src

ENV CGO_ENABLED=0 GOWORK=off

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .

COPY --from=web-builder /src/internal/entry/web/static ./internal/entry/web/static

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/ainovel-cli \
    ./cmd/ainovel-cli

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/expansion-auditor \
    ./cmd/expansion-auditor

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/manuscript-completion-auditor \
    ./cmd/manuscript-completion-auditor

FROM alpine:3.22

RUN apk add --no-cache \
    ca-certificates \
    tzdata

WORKDIR /workspace

ENV HOME=/home/ainovel

VOLUME ["/home/ainovel/.ainovel", "/var/lib/ainovel"]

COPY --from=builder /out/ainovel-cli /usr/local/bin/ainovel-cli
COPY --from=builder /out/expansion-auditor /usr/local/bin/expansion-auditor
COPY --from=builder /out/manuscript-completion-auditor /usr/local/bin/manuscript-completion-auditor
COPY --from=builder /src/LICENSE /usr/share/licenses/ainovel/LICENSE
COPY --from=builder /src/THIRD_PARTY_LICENSES.md /usr/share/licenses/ainovel/THIRD_PARTY_LICENSES.md

# Runtime is deliberately non-root. An administrator-owned bootstrap job must
# provision the mounted authority volume before this container starts.
RUN addgroup -g 65532 ainovel && \
    adduser -D -h /home/ainovel -u 65532 -G ainovel ainovel && \
    install -d -o 65532 -g 65532 -m 0700 /home/ainovel/.ainovel

EXPOSE 9898

USER 65532:65532

ENTRYPOINT ["ainovel-cli"]
CMD ["web", "--host", "0.0.0.0", "--port", "9898"]
