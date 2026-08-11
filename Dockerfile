FROM node:24-alpine AS web-build
WORKDIR /app
COPY package.json package-lock.json .npmrc ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:24-alpine AS web
WORKDIR /app
ENV NODE_ENV=production
COPY --from=web-build /app/package.json /app/package-lock.json ./
COPY --from=web-build /app/node_modules ./node_modules
COPY --from=web-build /app/dist ./dist
COPY --from=web-build /app/.vinext ./.vinext
COPY --from=web-build /app/public ./public
COPY --from=web-build /app/app ./app
COPY --from=web-build /app/vite.config.ts /app/next.config.ts ./
EXPOSE 3000
CMD ["npm", "run", "start"]
