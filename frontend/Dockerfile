# syntax=docker/dockerfile:1

FROM node:22-alpine AS base
ENV NEXT_TELEMETRY_DISABLED=1
WORKDIR /app

FROM base AS deps
COPY package.json package-lock.json* ./
# npm ci when a lockfile is committed (reproducible). If the lockfile is stale (for example right
# after a dependency was added to package.json) fall back to npm install so the build still works;
# then refresh and commit the lockfile (see README: "Upgrading to Phase 3").
RUN if [ -f package-lock.json ]; then npm ci || npm install; else npm install; fi

# Development image. docker-compose.yml bind-mounts ./frontend over /app and keeps
# node_modules and .next in container-owned volumes.
FROM base AS dev
COPY --from=deps --chown=node:node /app/node_modules ./node_modules
COPY --chown=node:node . .
RUN mkdir -p .next && chown node:node /app /app/.next
USER node
EXPOSE 3000
CMD ["npm", "run", "dev", "--", "--hostname", "0.0.0.0"]

FROM base AS builder
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Rewrites are baked in at build time, so the API address must be known here.
ARG API_PROXY_TARGET=http://backend:8000
ENV API_PROXY_TARGET=$API_PROXY_TARGET
RUN npm run build

# Production-style image: Next.js standalone server only.
FROM node:22-alpine AS prod
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
WORKDIR /app
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
USER node
EXPOSE 3000
CMD ["node", "server.js"]
