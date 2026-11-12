package preflight

// HTTP endpoints use real HTTP framing over net.Pipe. Tests need no listening
// sockets, Internet, GPU or access to the real link-local metadata service.
import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr("memory") }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type testServer struct {
	URL       string
	Client    *http.Client
	server    *http.Server
	transport *http.Transport
	listener  *pipeListener
}

func newTestServer(t *testing.T, handler http.Handler) *testServer {
	t.Helper()
	l := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	client, tr := directClient()
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		a, b := net.Pipe()
		select {
		case l.connections <- b:
			return a, nil
		case <-ctx.Done():
			a.Close()
			b.Close()
			return nil, ctx.Err()
		case <-l.done:
			a.Close()
			b.Close()
			return nil, net.ErrClosed
		}
	}
	s := &testServer{URL: "http://test.invalid", Client: client, server: &http.Server{Handler: handler}, transport: tr, listener: l}
	go func() { _ = s.server.Serve(l) }()
	return s
}
func (s *testServer) Close() {
	s.listener.Close()
	s.server.Close()
	s.transport.CloseIdleConnections()
}
