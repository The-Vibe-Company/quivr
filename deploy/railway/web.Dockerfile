FROM node:22-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da AS build
WORKDIR /app
COPY quivr-search/package*.json ./
RUN npm ci
COPY quivr-search ./
RUN npm run build

FROM node:22-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da
# Patch the fixed critical perl-base findings in the pinned upstream image.
RUN apt-get update \
 && apt-get install -y --no-install-recommends perl-base=5.36.0-7+deb12u4 \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /app/dist ./dist
# Every server module: server.mjs imports its siblings (feeds, feed, alerts, ...).
COPY quivr-search/*.mjs ./
ENV HOST=0.0.0.0 PORT=3000 DEMO_SECURE_COOKIE=true
USER node
CMD ["node", "server.mjs"]
