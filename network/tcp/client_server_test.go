package tcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
	"yola/network/internal/header"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/encoding/protojson"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

type kickHandler struct{}

func (kickHandler) Open(context.Context, network.Connection) error { return nil }
func (kickHandler) Handle(ctx context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		message.Op = v1.OpAuthReply
		return message, nil
	}
	_ = conn.CloseWithProto(ctx, &v1.Proto{Op: v1.OpKick, Code: 7})
	return message, nil
}
func (kickHandler) Close(context.Context, network.Connection) {}

type messageContextKey struct{}

type messageContextHandler struct {
	observed chan<- error
	endpoint string
}

func (*messageContextHandler) Open(context.Context, network.Connection) error { return nil }

func (h *messageContextHandler) Handle(ctx context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		return &v1.Proto{Op: v1.OpAuthReply, Seq: message.Seq}, nil
	}
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		h.observed <- errors.New("transport context is unavailable")
	} else {
		_, hasDeadline := ctx.Deadline()
		if tr.Operation() != network.ConnectionHandlerOperation || tr.Endpoint() != h.endpoint ||
			tr.RequestHeader().Get("remote_ip") != "127.0.0.1" ||
			tr.RequestHeader().Get(header.ConnectionIDKey) == "" || !hasDeadline ||
			ctx.Value(messageContextKey{}) != true {
			h.observed <- fmt.Errorf("unexpected message context: transport=%+v deadline=%t middleware=%v",
				tr, hasDeadline, ctx.Value(messageContextKey{}))
		} else {
			h.observed <- nil
		}
	}
	return &v1.Proto{
		Op:   v1.OpResponse,
		Seq:  message.Seq,
		Cmd:  message.Cmd,
		Code: 23,
		Body: []byte("replacement"),
	}, nil
}

func (*messageContextHandler) Close(context.Context, network.Connection) {}

func TestServerMessageContextAndReplacementReply(t *testing.T) {
	observed := make(chan error, 1)
	handler := &messageContextHandler{observed: observed}
	codec := encoding.GetCodec(protojson.Name)
	endpoint := startTCPTestServer(t, handler, Codec(codec), Timeout(time.Second), Middleware(
		func(next middleware.Handler) middleware.Handler {
			return func(ctx context.Context, request any) (any, error) {
				return next(context.WithValue(ctx, messageContextKey{}, true), request)
			}
		},
	))
	handler.endpoint = "tcp://" + endpoint
	client, err := NewClient(
		context.Background(),
		WithAddress(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithCodec(codec),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.Close)

	body, code, err := client.Request(context.Background(), 7, new(v1.ClientAuthReq))
	if err != nil {
		t.Fatalf("Client.Request() error = %v", err)
	}
	if string(body) != "replacement" || code != 23 {
		t.Errorf("Client.Request() = (%q, %d), want (%q, %d)", body, code, "replacement", 23)
	}
	if err := waitTCPValue(t, observed); err != nil {
		t.Errorf("handler message context error = %v", err)
	}
}

func TestClientReceivesFinalKick(t *testing.T) {
	kicked := make(chan *v1.Proto, 1)
	endpoint := startTCPTestServer(t, kickHandler{})
	client, err := NewClient(context.Background(), WithAddress(endpoint), WithServiceName("game"),
		WithToken("synthetic-token"), WithKickHandler(func(code int32) {
			kicked <- &v1.Proto{Code: code}
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	_, _, err = client.Request(context.Background(), 1, new(v1.ClientAuthReq))
	if err == nil {
		t.Fatal("Client.Request() succeeded after the final kick")
	}
	got := waitTCPValue(t, kicked)
	if got.Code != 7 {
		t.Fatalf("Kick() code = %d, want 7", got.Code)
	}
}

func TestClientConnectsWithTLS(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificateServer.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()
	endpoint := startTCPTestServer(t, tcpTestHandler{}, TLSConfig(serverTLS))
	connected := make(chan struct{})
	client, err := NewClient(context.Background(), WithAddress(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}), WithConnectFunc(func() { close(connected) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	waitTCPValue(t, connected)
	reply, code, err := client.Request(context.Background(), 1, &v1.Proto{Op: v1.OpPush})
	if err != nil || code != 0 || len(reply) == 0 {
		t.Fatalf("Request() = %x, %d, %v", reply, code, err)
	}
}
