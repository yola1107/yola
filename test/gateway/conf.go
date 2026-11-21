package main

import (
	"errors"
	"flag"
	"time"

	"yola/network/websocket"
)

type gatewayConfig struct {
	id              string
	grpcListen      string
	tcpListen       string
	wsListen        string
	wsMaxConns      int32
	wsMaxConnsPerIP int32
	logLevel        string
	rpcTimeout      time.Duration
	advertiseHost   string
	natsURL         string

	redisAddress  string
	redisPassword string
	redisDB       int
	etcdEndpoints string
	etcdPrefix    string
}

const defaultRemoteHost = "127.0.0.1"

func parseConfig() (*gatewayConfig, error) {
	cfg := new(gatewayConfig)
	var maxConns, maxConnsPerIP int
	flag.StringVar(&cfg.id, "id", "gateway-1", "Kratos instance ID")
	flag.StringVar(&cfg.grpcListen, "grpc-listen", "0.0.0.0:9010", "internal gRPC listen address")
	flag.StringVar(&cfg.tcpListen, "tcp-listen", "0.0.0.0:3101", "client TCP listen address")
	flag.StringVar(&cfg.wsListen, "ws-listen", "0.0.0.0:3102", "client WebSocket listen address")
	flag.IntVar(&maxConns, "ws-max-connections", websocket.DefaultMaxConnLimit, "maximum concurrent WebSocket connections")
	flag.IntVar(&maxConnsPerIP, "ws-max-connections-per-ip", websocket.DefaultMaxConnPerIP, "maximum concurrent WebSocket connections per IP")
	flag.StringVar(&cfg.logLevel, "log-level", "info", "log level: debug, info, warn, or error")
	flag.DurationVar(&cfg.rpcTimeout, "rpc-timeout", 3*time.Second, "WebSocket handler and internal RPC timeout")
	flag.StringVar(&cfg.advertiseHost, "advertise-host", "127.0.0.1", "gRPC advertise host")
	flag.StringVar(&cfg.natsURL, "nats-url", "nats://"+defaultRemoteHost+":4222", "NATS URL")
	flag.StringVar(&cfg.redisAddress, "redis-addr", defaultRemoteHost+":6379", "Redis address")
	flag.StringVar(&cfg.redisPassword, "redis-password", "", "Redis password; leave empty when Redis has no auth")
	flag.IntVar(&cfg.redisDB, "redis-db", 0, "Redis database [0, 15]")
	flag.StringVar(&cfg.etcdEndpoints, "etcd-endpoints", defaultRemoteHost+":2379", "comma-separated etcd endpoints")
	flag.StringVar(&cfg.etcdPrefix, "etcd-prefix", "/yola/test", "etcd registry prefix")
	flag.Parse()
	if maxConns <= 0 || maxConns > 1<<31-1 || maxConnsPerIP <= 0 || maxConnsPerIP > 1<<31-1 {
		return nil, errors.New("websocket connection limits must be within [1, 2147483647]")
	}
	cfg.wsMaxConns = int32(maxConns)
	cfg.wsMaxConnsPerIP = int32(maxConnsPerIP)
	if cfg.rpcTimeout <= 0 {
		return nil, errors.New("rpc timeout must be positive")
	}
	if cfg.redisDB < 0 || cfg.redisDB > 15 {
		return nil, errors.New("redis database must be within [0, 15]")
	}
	return cfg, nil
}
