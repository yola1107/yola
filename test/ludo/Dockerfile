FROM golang:1.26 AS builder

COPY . /src
WORKDIR /src/test

RUN go build -o /out/ludo-server ./ludo/cmd/ludo-server

FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
		ca-certificates \
		netbase \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/ludo-server /app/ludo-server

WORKDIR /app

EXPOSE 9001
VOLUME /data/conf

CMD ["./ludo-server", "-conf", "/data/conf"]
