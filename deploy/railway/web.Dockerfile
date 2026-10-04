FROM node:22-bookworm-slim AS build
WORKDIR /app
COPY quivr-search/package*.json ./
RUN npm ci
COPY quivr-search ./
RUN npm run build

FROM node:22-bookworm-slim
WORKDIR /app
COPY --from=build /app/dist ./dist
# Every server module: server.mjs imports its siblings (feeds, feed, alerts, ...).
COPY quivr-search/*.mjs ./
ENV HOST=0.0.0.0 PORT=3000 DEMO_SECURE_COOKIE=true
USER node
CMD ["node", "server.mjs"]
