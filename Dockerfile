FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/order-match-engine ./cmd/order-match-engine
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/demo ./cmd/demo

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/order-match-engine /order-match-engine
COPY --from=build /out/demo /demo
EXPOSE 50051 9090
ENTRYPOINT ["/order-match-engine"]
