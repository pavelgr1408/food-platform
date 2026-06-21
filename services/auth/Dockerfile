FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/auth-service ./cmd/auth-service

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/auth-service /app/auth-service

EXPOSE 8081

USER nonroot:nonroot

ENTRYPOINT ["/app/auth-service"]
