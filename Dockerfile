# ------------- stage 1 -------------
FROM golang:1.21-alpine AS builder
RUN apk update && apk add --no-cache upx 

WORKDIR /app
COPY . . 
RUN go mod tidy 
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o YourText && \
    upx YourText

# ------------- stage 2 -------------
FROM alpine:latest
WORKDIR /app 
COPY --from=builder /app/YourText .

ENV GIN_MODE=release
EXPOSE 8080
CMD ["./YourText"]
