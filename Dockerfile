# ------------- stage 1 -------------
# builder 固定在构建机的原生平台上运行，通过 GOOS/GOARCH 交叉编译，避免 QEMU 模拟
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /app
COPY . . 
RUN go mod tidy 
# arm/v7 的 TARGETVARIANT 为 v7，去掉前缀 v 后作为 GOARM；其他架构该值为空，会被忽略
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -ldflags="-s -w" -o YourText

# ------------- stage 2 -------------
FROM alpine:latest
WORKDIR /app 
COPY --from=builder /app/YourText .

ENV GIN_MODE=release
EXPOSE 8080
CMD ["./YourText"]
