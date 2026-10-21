# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG TARGETOS
ARG TARGETARCH
# 在构建机原生运行 Go 编译器，生成目标服务器架构的程序。
# 接口只使用 JSON，排除未使用且编译内存开销较大的 MsgPack 依赖。
# 缓存编译结果，修改源码后复用依赖；串行编译并显示包名。
# 并行限制仅作用于构建过程，不是内存硬上限，也不限制服务运行并发。
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOMAXPROCS=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -p 1 -v -tags=nomsgpack -trimpath -ldflags="-s -w" -o /app/backend . && \
    CGO_ENABLED=0 GOMAXPROCS=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -p 1 -v -trimpath -ldflags="-s -w" -o /app/worker ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app
COPY --from=build /app/backend ./backend
COPY --from=build /app/worker ./worker
COPY frontend ./frontend
USER app
EXPOSE 8080
CMD ["./backend"]
