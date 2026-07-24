FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/clawfiles \
    ./cmd/clawfiles

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/clawfiles /clawfiles

EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/clawfiles"]
