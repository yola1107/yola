package nats

import (
	"context"
	"errors"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestPublishDeadlineDuringNativeWrite(t *testing.T) {
	bus, writer, release := newBlockedWriteBus(t)
	writer.armed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	published := make(chan error, 1)
	go func() {
		published <- bus.Publish(ctx, event.Event{
			Topic: "yola.event.blocked-publish", Payload: make([]byte, 64<<10),
		})
	}()
	waitSignal(t, writer.entered, "publish did not reach the write barrier")
	select {
	case err := <-published:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		release()
		_ = waitPublishResult(t, published)
		t.Fatal("Publish exceeded its caller deadline during native write")
	}
	release()
}

func TestCanceledPublishWaitingForWorkerIsNeverSent(t *testing.T) {
	bus, writer, release := newBlockedWriteBus(t)
	const topic = "yola.event.canceled-publish"
	sub, err := bus.conn.SubscribeSync(topic)
	require.NoError(t, err)
	require.NoError(t, bus.conn.Flush())
	writer.armed.Store(true)
	first := make(chan error, 1)
	go func() {
		first <- bus.Publish(context.Background(), event.Event{
			Topic: topic, Payload: make([]byte, 64<<10),
		})
	}()
	waitSignal(t, writer.entered, "first publish did not block")

	const callers = 32
	results := make(chan error, callers)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for range callers {
		go func() {
			results <- bus.Publish(ctx, event.Event{Topic: topic, Payload: []byte("canceled")})
		}()
	}
	for range callers {
		require.ErrorIs(t, waitPublishResult(t, results), context.DeadlineExceeded)
	}
	release()
	require.NoError(t, waitPublishResult(t, first))
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: topic, Payload: []byte("after")}))
	require.NoError(t, bus.conn.Flush())
	firstMessage, err := sub.NextMsg(time.Second)
	require.NoError(t, err)
	require.Len(t, firstMessage.Data, 64<<10)
	lastMessage, err := sub.NextMsg(time.Second)
	require.NoError(t, err)
	require.Equal(t, "after", string(lastMessage.Data))
	_, err = sub.NextMsg(20 * time.Millisecond)
	require.ErrorIs(t, err, natsgo.ErrTimeout, "canceled requests must not publish later")
}

func TestCloseReleasesPublishCallersAndJoinsWorker(t *testing.T) {
	bus, writer, release := newBlockedWriteBus(t)
	writer.armed.Store(true)
	published := make(chan error, 1)
	go func() {
		published <- bus.Publish(context.Background(), event.Event{
			Topic: "yola.event.close-publish", Payload: make([]byte, 64<<10),
		})
	}()
	waitSignal(t, writer.entered, "publish did not reach the write barrier")
	closed := make(chan error, 1)
	go func() { closed <- bus.Close() }()
	require.ErrorIs(t, waitPublishResult(t, published), event.ErrClosed)
	select {
	case <-closed:
		t.Fatal("Close returned before the native write finished")
	case <-bus.publishDone:
		t.Fatal("publish worker exited before the native write finished")
	default:
	}
	release()
	require.NoError(t, waitPublishResult(t, closed))
	select {
	case <-bus.publishDone:
	default:
		t.Fatal("Close did not join its publish worker")
	}
}

func TestPublisherDiscardsCanceledHandoff(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	const topic = "yola.event.canceled-handoff"
	sub, err := bus.conn.SubscribeSync(topic)
	require.NoError(t, err)
	require.NoError(t, bus.conn.Flush())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := publishRequest{
		ctx:    ctx,
		event:  event.Event{Topic: topic},
		result: make(chan error, 1),
	}
	// 模拟交接与取消同时就绪、select 选中交接的情况，验证 worker 仍会拒绝发送。
	select {
	case bus.publishRequests <- request:
	case <-time.After(time.Second):
		t.Fatal("publisher did not accept the handoff")
	}
	require.ErrorIs(t, waitPublishResult(t, request.result), context.Canceled)
	require.NoError(t, bus.conn.Flush())
	_, err = sub.NextMsg(20 * time.Millisecond)
	require.ErrorIs(t, err, natsgo.ErrTimeout)
}

func TestPublishTransfersPayloadOwnershipBeforeCancellation(t *testing.T) {
	// 暂停接收端，直接观察交接后的消息；caller 返回后可立即覆写原 Payload。
	bus := &Bus{
		ctx:             context.Background(),
		maxPayloadBytes: 4,
		publishRequests: make(chan publishRequest),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	result := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		waitSignal(t, done, "Publish caller did not exit")
	})
	payload := []byte("old")
	go func() {
		defer close(done)
		result <- bus.Publish(ctx, event.Event{Topic: "yola.event.payload", Payload: payload})
	}()
	var request publishRequest
	select {
	case request = <-bus.publishRequests:
	case <-time.After(time.Second):
		t.Fatal("Publish did not hand off the message")
	}
	cancel()
	require.ErrorIs(t, waitPublishResult(t, result), context.Canceled)
	copy(payload, "new")
	require.Equal(t, "old", string(request.event.Payload))
}

func TestConcurrentPublishAndClose(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	const callers = 32
	results := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			<-start
			results <- bus.Publish(context.Background(), event.Event{Topic: "yola.event.concurrent-publish"})
		}()
	}
	close(start)
	require.NoError(t, bus.Close())
	for range callers {
		err := waitPublishResult(t, results)
		require.True(t, err == nil || errors.Is(err, event.ErrClosed), "unexpected Publish result: %v", err)
	}
}

func waitPublishResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("publish operation did not finish")
		return nil
	}
}
