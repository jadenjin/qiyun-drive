FROM node:24-alpine AS web-build
WORKDIR /app
ENV NODE_OPTIONS=--max-old-space-size=768
COPY package.json package-lock.json .npmrc ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:24-alpine AS web
WORKDIR /app
ENV NODE_ENV=production
RUN apk upgrade --no-cache \
 && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack /opt/yarn-*
COPY --from=web-build --chown=node:node /app/package.json /app/package-lock.json ./
COPY --from=web-build --chown=node:node /app/node_modules ./node_modules
COPY --from=web-build --chown=node:node /app/dist ./dist
COPY --from=web-build --chown=node:node /app/.vinext ./.vinext
COPY --from=web-build --chown=node:node /app/public ./public
COPY --from=web-build --chown=node:node /app/app ./app
COPY --from=web-build --chown=node:node /app/vite.config.ts /app/next.config.ts ./
EXPOSE 3000
USER node
CMD ["./node_modules/.bin/vinext", "start"]
