# ----- 多阶段构建：构建 4 个独立二进制并打入同一镜像 -----
FROM golang:1.24-alpine AS builder

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG BUILD_TIME=unknown

RUN go build -trimpath -ldflags="-s -w \
        -X github.com/kleinai/backend/pkg/version.Build=${VERSION} \
        -X github.com/kleinai/backend/pkg/version.Time=${BUILD_TIME}" \
    -o /out/api    ./cmd/api  && \
    go build -trimpath -ldflags="-s -w \
        -X github.com/kleinai/backend/pkg/version.Build=${VERSION}" \
    -o /out/admin  ./cmd/admin && \
    go build -trimpath -ldflags="-s -w \
        -X github.com/kleinai/backend/pkg/version.Build=${VERSION}" \
    -o /out/openai ./cmd/openai && \
    go build -trimpath -ldflags="-s -w \
        -X github.com/kleinai/backend/pkg/version.Build=${VERSION}" \
    -o /out/worker ./cmd/worker && \
    go build -trimpath -ldflags="-s -w \
        -X github.com/kleinai/backend/pkg/version.Build=${VERSION}" \
    -o /out/agent  ./cmd/agent

# 准备运行期可写目录（logs / storage），在 builder 阶段就以 nonroot UID 65532
# 创建好；distroless 没有 mkdir/chown，且 docker 会把 named volume 的初始属主
# 继承自镜像里目标路径的属主，这里预创建避免容器以 nonroot 启动后写不进去。
RUN mkdir -p /out/empty/logs /out/empty/storage && \
    chown -R 65532:65532 /out/empty

# ----- 运行阶段：distroless · 非 root -----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo                 /usr/share/zoneinfo
COPY --from=builder /out/api                            /app/api
COPY --from=builder /out/admin                          /app/admin
COPY --from=builder /out/openai                         /app/openai
COPY --from=builder /out/worker                         /app/worker
COPY --from=builder /out/agent                          /app/agent
COPY --from=builder /src/configs                        /app/configs
COPY --from=builder --chown=65532:65532 /out/empty/logs    /app/logs
COPY --from=builder --chown=65532:65532 /out/empty/storage /app/storage

ENV TZ=Asia/Shanghai
USER 65532:65532

EXPOSE 17180 17188 17200

CMD ["/app/api"]
