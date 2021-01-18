package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	log "github.com/golang/glog"

	tpb "github.com/openconfig/grpctunnel/proto/tunnel"
	"github.com/openconfig/grpctunnel/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServerConfig defines tunnel server setup.
type ServerConfig struct {
	Addr, CertFile, KeyFile string
}

// Conn is a wraper as a net.Conn interface.
type Conn struct {
	conn     io.ReadWriteCloser
	addr     string
	ts       *tunnel.Server
	targetID string
}

func (tc *Conn) Read(b []byte) (n int, err error) {
	return tc.conn.Read(b)
}
func (tc *Conn) Write(b []byte) (n int, err error) {
	return tc.conn.Write(b)
}

// Close connections.
func (tc *Conn) Close() error {
	return tc.conn.Close()
}

// LocalAddr is trivial implementation.
func (tc *Conn) LocalAddr() net.Addr { return nil }

// RemoteAddr is trivial implementation.
func (tc *Conn) RemoteAddr() net.Addr { return nil }

// SetDeadline is trivial implementation.
func (tc *Conn) SetDeadline(t time.Time) error { return nil }

// SetReadDeadline is trivial implementation.
func (tc *Conn) SetReadDeadline(t time.Time) error { return nil }

// SetWriteDeadline is trivial implementation.
func (tc *Conn) SetWriteDeadline(t time.Time) error { return nil }

// Server initiates a tunnel server, and passes all the received targets .
func Server(ctx context.Context, conf *ServerConfig, chServer chan *tunnel.Server, chErr chan error, chTarget chan string) {

	var opts []grpc.ServerOption
	if conf.CertFile != "" && conf.KeyFile != "" {
		creds, err := credentials.NewServerTLSFromFile(conf.CertFile, conf.KeyFile)
		if err != nil {
			chErr <- fmt.Errorf("failed to accept connection: %v", err)
			return
		}
		opts = append(opts, grpc.Creds(creds))
	}
	s := grpc.NewServer(opts...)
	defer s.Stop()

	tagRegHandler := func(ts []string) error {
		log.Infof("handling the received targets: %v", ts)
		for i, t := range ts {
			log.Infof("registered %s: %s", i, t)
			chTarget <- t
		}
		log.Info("target reg finished")
		return nil
	}

	ts, err := tunnel.NewServer(tunnel.ServerConfig{TargetRegisterHandler: tagRegHandler})

	if err != nil {
		chErr <- fmt.Errorf("failed to create new server: %v", err)
		return
	}
	tpb.RegisterTunnelServer(s, ts)

	l, err := net.Listen("tcp", conf.Addr)
	if err != nil {
		chErr <- fmt.Errorf("failed to create listener: %v", err)
		return
	}
	defer l.Close()

	errCh := make(chan error, 2)

	go func() {
		chServer <- ts
		if err := s.Serve(l); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		chErr <- ctx.Err()
		return
	case err := <-errCh:
		chErr <- err
		return
	}
}

// ServerConn tries and returns a tunnel connection.
func ServerConn(ctx context.Context, ts *tunnel.Server, addr string, target string) (*Conn, error) {
	var tc Conn

	for {
		session, err := ts.NewSession(ctx, tunnel.ServerSession{TargetID: target})
		if err == nil {
			tc.conn = session
			tc.targetID = target
			tc.addr = addr
			return &tc, nil
		}
		time.Sleep(time.Second)
		log.Infof("Failed to get tunnel connection: %v.\nRetrying in 1 sec.", err)
	}
}

func startTunnelClient(ctx context.Context, addr string, cert string, chIO chan io.ReadWriteCloser,
	chErr chan error, started chan bool, targets *[]string) {
	log.Info("starting tunnel client")

	opts := []grpc.DialOption{grpc.WithDefaultCallOptions()}
	if cert == "" {
		opts = append(opts, grpc.WithInsecure())
	} else {
		creds, err := credentials.NewClientTLSFromFile(cert, "")
		if err != nil {
			chErr <- fmt.Errorf("failed to load credentials: %v", err)
			return
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	}
	clientConn, err := grpc.Dial(addr, opts...)
	log.Info("tunnel client connection dialed")
	if err != nil {
		chErr <- fmt.Errorf("grpc dial error: %v", err)
		return
	}
	defer clientConn.Close()

	registerHandler := func(u string) error {

		log.Infof("reg handler received id: %v", u)
		return nil
	}

	handler := func(id string, i io.ReadWriteCloser) error {
		log.Infof("handler called for id:%v", id)
		chIO <- i
		return nil
	}

	client, err := tunnel.NewClient(tpb.NewTunnelClient(clientConn), tunnel.ClientConfig{
		RegisterHandler: registerHandler,
		Handler:         handler,
	}, targets)
	if err != nil {
		chErr <- fmt.Errorf("failed to create tunnel client: %v", err)
		return
	}

	err = client.Run(ctx)
	if err != nil {
		chErr <- err
		return
	}
}

// Listener wraps a tunnel connection.
type Listener struct {
	conn  io.ReadWriteCloser
	addr  tunnelAddr
	chIO  chan io.ReadWriteCloser
	chErr chan error
}

// Accept waits and returns a tunnel connection.
func (l *Listener) Accept() (net.Conn, error) {
	if l.conn != nil {
		conn := l.conn
		l.conn = nil
		return &Conn{conn: conn}, nil
	}

	select {
	case err := <-l.chErr:
		return nil, fmt.Errorf("failed to get tunnel listener: %v", err)
	case l.conn = <-l.chIO:
		log.Infof("tunnel listen setup")
		return &Conn{conn: l.conn}, nil
	}
}

// Close close the embedded connection. Will need more implementation to handle multiple connections.
func (l *Listener) Close() error {
	if l.conn != nil {
		return l.conn.Close()
	}
	return nil
}

// Addr is a trivial implementation.
func (l *Listener) Addr() net.Addr {
	return l.addr
}

type tunnelAddr struct {
	network string
	address string
}

func (a tunnelAddr) Network() string { return a.network }
func (a tunnelAddr) String() string  { return a.address }

// Listen create a tunnel client and returns a Listener.
func Listen(ctx context.Context, addr string, cert string, targets *[]string) (net.Listener, error) {
	l := Listener{}
	l.addr = tunnelAddr{network: "tcp", address: addr}
	l.chErr = make(chan error)
	l.chIO = make(chan io.ReadWriteCloser)
	started := make(chan bool)
	for {
		go startTunnelClient(ctx, addr, cert, l.chIO, l.chErr, started, targets)

		// tunnel client establishes a tunnel session if it succeeded.
		// retry if it fails.
		select {
		case err := <-l.chErr:
			log.Infof("failed to get tunnel listener: %v", err)
		// conn is obtained here first, instead of in Accept, because the tunnel handler is called during client setup,
		// which is blocking. l.Accept will continue waiting for l.conn if there are additional tunnel session(s).
		case l.conn = <-l.chIO:
			log.Infof("tunnel listen setup")
			return &l, nil
		}
		time.Sleep(time.Second)
		log.Info("Retrying in 1 sec.")
	}
}
