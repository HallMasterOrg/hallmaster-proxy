FROM oven/bun:1.3.2-alpine AS bun

RUN apk add --no-cache libstdc++ g++

WORKDIR /app

COPY ./package.json ./bun.lock ./tsconfig.json ./

RUN /usr/local/bin/bun install --production --frozen-lockfile

COPY ./src/ ./src/

ENTRYPOINT [ "/usr/local/bin/bun", "run", "src/shardManager.ts" ]
