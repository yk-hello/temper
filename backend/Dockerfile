# 构建
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/qs ./cmd/qs

# 运行
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir -p /data && chown app /data
# /data：自动生成的主密钥放这里（compose 里挂数据卷）
VOLUME /data
USER app
COPY --from=build /out/qs /usr/local/bin/qs
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8000/api/health >/dev/null || exit 1
ENTRYPOINT ["qs"]
