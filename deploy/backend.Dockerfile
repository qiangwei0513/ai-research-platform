FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# 小规格服务器串行编译依赖，降低构建压力；-v 显示正在编译的包。
# 环境变量仅作用于构建命令，不限制最终 Go 服务的运行并发。
RUN CGO_ENABLED=0 GOMAXPROCS=1 go build -p 1 -v -trimpath -ldflags="-s -w" -o /app/backend .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app
COPY --from=build /app/backend ./backend
COPY frontend ./frontend
USER app
EXPOSE 8080
CMD ["./backend"]
