FROM node:24.16-alpine3.23

RUN apk add --no-cache \
        bash \
        curl \
        socat \
        make

WORKDIR /app

COPY ui/package.json ui/package-lock.json ./
RUN npm install

CMD npm run dev -- --host 0.0.0.0 --port $PORT
