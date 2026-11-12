FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /arena ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /arena /arena
EXPOSE 8080
ENTRYPOINT ["/arena"]
CMD ["-addr", ":8080", "-origins", "localhost:*,127.0.0.1:*,arena-client.mmamatzhanov.workers.dev", "-ip-header", "Fly-Client-IP"]