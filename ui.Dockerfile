FROM node:22.11-alpine3.20

RUN apk add --no-cache \
        bash \
        curl \
        socat \
        make

WORKDIR /app

COPY ui/package.json ui/package-lock.json ./
RUN npm install

CMD npm run dev -- --host 0.0.0.0 --port $PORT
