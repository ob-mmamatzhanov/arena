FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /arena ./cmd/server

# Клиент живёт в отдельном репозитории. ADD по URL сверяет содержимое архива,
# поэтому новый коммит в main клиента инвалидирует кэш слоя.
FROM golang:1.27-alpine AS client
ARG CLIENT_REF=main
ADD https://github.com/ob-mmamatzhanov/arena-client/archive/refs/heads/${CLIENT_REF}.tar.gz /client.tar.gz
RUN mkdir /web && tar -xzf /client.tar.gz -C /web --strip-components=1 && rm -f /web/.gitignore

FROM gcr.io/distroless/static-debian12
COPY --from=build /arena /arena
COPY --from=client /web /web
EXPOSE 8080
ENTRYPOINT ["/arena"]
CMD ["-addr", ":8080", "-origins", "localhost:*,127.0.0.1:*", "-static", "/web"]
