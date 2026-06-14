FROM node:22-alpine AS web-build
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS go-build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/internal/api/static/ui-v2 ./internal/api/static/ui-v2
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aexp ./cmd/aexp

FROM alpine:3.23
RUN apk add --no-cache bash ca-certificates netcat-openbsd openssh-client tzdata
WORKDIR /data/aexp
COPY --from=go-build /out/aexp /usr/local/bin/aexp
EXPOSE 8080
VOLUME ["/data/aexp"]
ENTRYPOINT ["/usr/local/bin/aexp"]
CMD ["serve", "--host", "0.0.0.0", "--port", "8080", "--db", "/data/aexp/aexp.db"]
