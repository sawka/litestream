FROM golang:1.15 as build

WORKDIR /go/src/github.com/benbjohnson/litestream
COPY . .

RUN go install -ldflags "-linkmode external -extldflags -static" ./cmd/litestream

FROM gcr.io/distroless/base
COPY --from=build /go/bin/litestream /

ENTRYPOINT ["/litestream"]
CMD []