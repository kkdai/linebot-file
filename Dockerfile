# 多階段建置，產生輕量的執行映像檔
# ---- 階段 1: 建置 ----
FROM golang:1.24-alpine AS builder

WORKDIR /app

# 先複製 go module 檔案，讓相依套件層可以被 Docker 快取
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 產生靜態連結執行檔，-ldflags="-s -w" 縮小體積
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/server .

# ---- 階段 2: 執行 ----
FROM alpine:3.20

# 本服務全程以 HTTPS 呼叫 Google API，alpine 預設不含根憑證
RUN apk add --no-cache ca-certificates

# 以非 root 身分執行
RUN adduser -D -u 10001 appuser
USER appuser

WORKDIR /app
COPY --from=builder /app/server /app/server

# Cloud Run 會以 PORT 環境變數指定連接埠，預設 8080
ENV PORT=8080
EXPOSE 8080

CMD ["/app/server"]
