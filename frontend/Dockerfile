# web-app: сборка Vite → nginx (unprivileged). Контекст — корень репозитория:
#   docker build -f frontend/Dockerfile -t food/web-app:dev .
# /api/* проксируется на gateway (frontend/nginx.conf.template), поэтому браузеру CORS не нужен.
FROM node:22-alpine AS build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
COPY frontend/packages/api/package.json packages/api/package.json
COPY frontend/packages/shared/package.json packages/shared/package.json
COPY frontend/packages/design-tokens/package.json packages/design-tokens/package.json
COPY frontend/apps/web-app/package.json apps/web-app/package.json
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
ARG VITE_API_MODE=real
ARG VITE_PAYMENT_MODE=redirect
ENV VITE_API_MODE=$VITE_API_MODE VITE_PAYMENT_MODE=$VITE_PAYMENT_MODE VITE_API_BASE_URL=/api
RUN npm run build --workspace web-app

FROM nginxinc/nginx-unprivileged:1.27-alpine
ENV GATEWAY_UPSTREAM=http://gateway:8080 NGINX_RESOLVER=127.0.0.11
COPY frontend/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /src/frontend/apps/web-app/dist /usr/share/nginx/html
EXPOSE 3000
