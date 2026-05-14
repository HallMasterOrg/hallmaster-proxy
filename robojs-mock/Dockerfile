FROM node:lts-trixie

WORKDIR /app

COPY package.json package-lock.json ./

RUN npm ci --legacy-peer-deps

COPY . .

RUN npx robo build

RUN mkdir -p .robo/flashcore && chown -R node:node .robo

ENV NODE_ENV=production
ENV NODE_OPTIONS="--enable-source-maps"

EXPOSE 3000
VOLUME /app/.robo/flashcore

USER node
CMD ["npx", "robo", "mock", "start", "--port", "3000"]
