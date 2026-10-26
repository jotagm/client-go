# Imagem do Client_GO. O build roda na arquitetura de quem builda ($BUILDPLATFORM) e só o
# binário sai para a do destino ($TARGETARCH, arm64 na EC2 t4g): Go cruza compila sem emulação,
# então nada de QEMU rodando o compilador.
FROM --platform=$BUILDPLATFORM golang:1.24 AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /client-go ./cmd/client-go

# distroless/static: sem shell, sem gerenciador de pacotes, com certificados de CA (o SDK da
# AWS precisa deles) e usuário não-root. O healthcheck é o próprio binário (subcomando "sonda").
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /client-go /client-go
ENV AVISO_ENDERECO=:8081
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/client-go", "sonda"]
ENTRYPOINT ["/client-go"]
