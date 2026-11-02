# Imagen de la Lambda de señalización. Se construye desde la raíz del repo
# (necesita core/):  docker build -f signaling/lambda/Dockerfile -t rda-signaling .
#
# Go 1.26: es la versión que fija go.mod (la que pide gomobile para la APK).
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY core ./core
COPY signaling ./signaling
# CGO apagado: binario estático para el runtime "provided" de Lambda.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bootstrap ./signaling/lambda

# Runtime OS-only de Lambda: ejecuta /var/runtime/bootstrap o el CMD.
FROM public.ecr.aws/lambda/provided:al2023
COPY --from=builder /bootstrap /var/runtime/bootstrap
CMD ["bootstrap"]
