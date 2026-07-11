FROM golang:1.25.12-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/alertlens ./cmd/alertlens

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/alertlens /alertlens
ENTRYPOINT ["/alertlens"]
