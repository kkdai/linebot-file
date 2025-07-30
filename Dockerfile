# 使用多階段建置來建立一個輕量的映像檔
# 階段 1: 建置
FROM golang:1.24-alpine AS builder

WORKDIR /app

# 先複製 go module 檔案並下載相依套件
# 這樣可以利用 Docker 的快取機制，只有在相依套件變更時才重新下載
COPY go.mod go.sum ./
RUN go mod download

# 複製所有原始碼
COPY . .

# 建置 Go 應用程式
# -ldflags="-s -w" 可以縮小執行檔的大小
# CGO_ENABLED=0 確保產生靜態連結的執行檔
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/server .

# 階段 2: 運行
FROM alpine:latest

WORKDIR /app

# 從 builder 階段複製編譯好的執行檔
COPY --from=builder /app/server /app/server

# Cloud Run 會自動提供 PORT 環境變數，預設為 8080
EXPOSE 8080

# 執行應用程式
CMD ["/app/server"]
