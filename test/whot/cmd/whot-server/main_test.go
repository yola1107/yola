package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"yola/node"
	"yola/test/internal/registry/etcd"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
)

func TestNewAppReleasesNodeAfterRegistrationFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	drainContexts := make(chan context.Context, 1)
	var drainCalls atomic.Int32
	server, err := node.NewServer(node.Listener(listener), node.Drain(func(ctx context.Context) error {
		drainCalls.Add(1)
		drainContexts <- ctx
		return ctx.Err()
	}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	startupErr := errors.New("register failed")
	registrar := &failedRegistrar{err: startupErr}
	app, cleanup := newApp(
		"node-test",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		server,
		&etcd.Registry{Registrar: registrar},
	)
	t.Cleanup(cleanup)

	require.ErrorIs(t, app.Run(), startupErr)
	cleanup()
	require.ErrorIs(t, registrar.ctx.Err(), context.Canceled)
	require.NoError(t, server.Stop(context.Background()))
	require.Equal(t, int32(1), drainCalls.Load())
	select {
	case ctx := <-drainContexts:
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now(), deadline, stopTimeout)
		info, ok := kratos.FromContext(ctx)
		require.True(t, ok)
		require.Equal(t, "node-test", info.ID())
	default:
		t.Fatal("application cleanup did not drain Node")
	}
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err, "startup failure must release the Node listener")
	require.NoError(t, rebound.Close())
}

func TestNewAppCleanupPreservesFailedDrain(t *testing.T) {
	drainErr := errors.New("drain failed")
	var drainCalls int
	server, err := node.NewServer(node.Drain(func(context.Context) error {
		drainCalls++
		return drainErr
	}))
	require.NoError(t, err)
	var logs bytes.Buffer
	_, cleanup := newApp("node-test", slog.New(slog.NewJSONHandler(&logs, nil)), server, nil)
	t.Cleanup(cleanup)

	cleanup()
	cleanup()
	require.Equal(t, 1, drainCalls)
	require.ErrorIs(t, server.Stop(context.Background()), drainErr)
	require.Contains(t, logs.String(), drainErr.Error())
}

type failedRegistrar struct {
	err error
	ctx context.Context
}

func (r *failedRegistrar) Register(ctx context.Context, _ *registry.ServiceInstance) error {
	r.ctx = ctx
	return r.err
}

func (*failedRegistrar) Deregister(context.Context, *registry.ServiceInstance) error { return nil }
