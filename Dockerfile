# syntax=docker/dockerfile:1

# Etap budowania.
#
# Obraz golang:1.25, a nie 1.24: github.com/modelcontextprotocol/go-sdk w wersji
# v1.7.0-pre.3 deklaruje w go.mod wymaganie "go 1.25.0", więc starszy toolchain
# nie zbuduje tego projektu.
FROM golang:1.25 AS build

WORKDIR /src

# Najpierw same pliki modułów — warstwa z zależnościami zmienia się rzadko
# i zostaje w cache przy każdej zmianie kodu.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Dopiero teraz reszta źródeł.
COPY . .

ARG WERSJA=dev

# CGO_ENABLED=0 daje statyczny plik binarny, który zadziała na distroless/static.
# -trimpath usuwa ścieżki budowania, -s -w obcina tablice symboli i debug.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.wersja=${WERSJA}" \
      -o /out/systim-mcp \
      ./cmd/systim-mcp

# Sprawdzenie poprawności testów jest w CI, nie tutaj — build ma być szybki.
# Katalog na PDF-y tworzymy w etapie budowania, bo distroless nie ma powłoki
# ani mkdir, a proces działa jako użytkownik bez prawa zapisu w /.
RUN mkdir -p /out/data/faktury && chown -R 65532:65532 /out/data

# Etap finalny.
#
# distroless/static-debian12 zawiera certyfikaty CA (potrzebne, bo łączymy się
# po HTTPS z Systim i z serwerem autoryzacji) oraz /etc/passwd z użytkownikiem
# nonroot, a nie ma powłoki ani menedżera pakietów.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/systim-mcp /usr/local/bin/systim-mcp
COPY --from=build --chown=65532:65532 /out/data /data

# nonroot to UID 65532 w obrazach distroless.
USER 65532:65532

WORKDIR /

EXPOSE 8000

VOLUME ["/data/faktury"]

ENV SYSTIM_TRANSPORT=http \
    SYSTIM_ADDR=:8000 \
    SYSTIM_KATALOG_PDF=/data/faktury \
    LOG_LEVEL=info

# Obraz nie ma powłoki ani curl-a, więc sondę realizuje sam plik binarny.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/systim-mcp", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/systim-mcp"]
