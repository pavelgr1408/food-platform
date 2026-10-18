# Единый Dockerfile для всех сервисов монорепо (multi-stage, distroless, nonroot).
#   docker build -f deploy/docker/Dockerfile --build-arg SRC=services/auth --build-arg TARGET=./services/auth/cmd/auth-service -t food/auth .
# Контекст сборки — корень репозитория. Кэш зависимостей отделён от исходников.
ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-alpine AS build
ARG SRC
ARG TARGET
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY ${SRC} ./${SRC}
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ${TARGET}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
