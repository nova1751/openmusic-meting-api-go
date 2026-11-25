# syntax=docker/dockerfile:1
FROM node:24-alpine AS source
ARG UPSTREAM_REVISION=fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5
RUN apk add --no-cache git
WORKDIR /app
RUN git init \
    && git fetch --depth=1 https://github.com/qq01-hub/OpenMusic-Meting-API.git "$UPSTREAM_REVISION" \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "$UPSTREAM_REVISION" \
    && rm -rf .git \
    && npm ci --omit=dev --no-audit --no-fund

FROM node:24-alpine
WORKDIR /app
RUN apk add --no-cache chromium nss freetype harfbuzz ca-certificates \
    && addgroup -S -g 1010 meting && adduser -S -D -u 1010 -G meting meting \
    && install -d -m 0700 -o 1010 -g 1010 /app/data
COPY --from=source /app /app
COPY --chmod=0444 compat/bridge.mjs /app/bridge.mjs
USER 1010:1010
ENV PORT=3000 DATA_DIR=/app/data PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium-browser
EXPOSE 3000
CMD ["node", "/app/bridge.mjs"]
