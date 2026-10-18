package listener

import (
	"context"
	"errors"
	"net"
	"sync"
)

const (
	_defaultNetwork = "tcp"
	_defaultAddress = ":0"
)

// Owner 按需绑定 listener，并独立于服务库负责关闭它。
type Owner struct {
	mu         sync.Mutex
	network    string
	address    string
	listener   net.Listener
	closed     bool
	accepting  chan struct{}
	acceptOnce sync.Once
}

func New(network, address string, listener net.Listener) *Owner {
	if network == "" {
		network = _defaultNetwork
	}
	if address == "" {
		address = _defaultAddress
	}
	return &Owner{
		network:   network,
		address:   address,
		listener:  listener,
		accepting: make(chan struct{}),
	}
}

// Accepting 在服务进入首次 Accept 后关闭；仅绑定地址不表示服务已启动。
func (o *Owner) Accepting() <-chan struct{} { return o.accepting }

// Prepare 只绑定一次配置地址；外部传入的 listener 视为已准备。
func (o *Owner) Prepare(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return net.ErrClosed
	}
	if o.listener != nil {
		return nil
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, o.network, o.address)
	if err != nil {
		return err
	}
	o.listener = listener
	return nil
}

func (o *Owner) Accept() (net.Conn, error) {
	if err := o.Prepare(context.Background()); err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, net.ErrClosed
	}
	listener := o.listener
	o.acceptOnce.Do(func() { close(o.accepting) })
	o.mu.Unlock()
	return listener.Accept()
}

func (o *Owner) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	listener := o.listener
	o.mu.Unlock()
	if listener == nil {
		return nil
	}
	err := listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (o *Owner) Addr() net.Addr {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.listener != nil {
		return o.listener.Addr()
	}
	return configuredAddr{network: o.network, address: o.address}
}

type configuredAddr struct {
	network string
	address string
}

func (a configuredAddr) Network() string { return a.network }

func (a configuredAddr) String() string { return a.address }
