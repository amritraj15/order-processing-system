FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-s -w" -o /orders ./cmd/orders

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 orders
USER orders
COPY --from=build /orders /usr/local/bin/orders
EXPOSE 8080
ENTRYPOINT ["orders"]
CMD ["server"]
