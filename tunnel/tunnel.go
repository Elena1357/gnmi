package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	log "github.com/golang/glog"

	"github.com/openconfig/gnmi/client"
	tpb "github.com/openconfig/grpctunnel/proto/tunnel"
	"github.com/openconfig/grpctunnel/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TunnelConfig is for tunnels. TODO(jxxu): Cleanup once tested.
type TunnelConfig struct {
	TunnelAddress, CertFile, KeyFile, Target string
}

// TunnelConn is a wraper.
type TunnelConn struct {
	conn io.ReadWriteCloser
	d    *client.Destination
	ts   *tunnel.Server
}

func (tc TunnelConn) Read(b []byte) (n int, err error)  { return tc.conn.Read(b) }
func (tc TunnelConn) Write(b []byte) (n int, err error) { return tc.conn.Write(b) }
func (tc TunnelConn) Close() error {
	return tc.conn.Close()
}
func (tc TunnelConn) LocalAddr() net.Addr                { return nil }
func (tc TunnelConn) RemoteAddr() net.Addr               { return nil }
func (tc TunnelConn) SetDeadline(t time.Time) error      { return nil }
func (tc TunnelConn) SetReadDeadline(t time.Time) error  { return nil }
func (tc TunnelConn) SetWriteDeadline(t time.Time) error { return nil }

func TunnelServer(ctx context.Context, d client.Destination, chTunnel chan *tunnel.Server, ch chan error, started chan bool) {
	conf := TunnelConfig{CertFile: "/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.crt",
		KeyFile: "/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.key",
		Target:  "target1"}
	var opts []grpc.ServerOption
	if conf.CertFile != "" && conf.KeyFile != "" {
		creds, err := credentials.NewServerTLSFromFile(conf.CertFile, conf.KeyFile)
		if err != nil {
			ch <- fmt.Errorf("failed to accept connection: %v", err)
			return
		}
		opts = append(opts, grpc.Creds(creds))
	}
	s := grpc.NewServer(opts...)
	defer s.Stop()
	ts, err := tunnel.NewServer(tunnel.ServerConfig{})
	chTunnel <- ts
	if err != nil {
		ch <- fmt.Errorf("failed to create new server: %v", err)
		return
	}
	tpb.RegisterTunnelServer(s, ts)

	l, err := net.Listen("tcp", d.Addrs[0])
	// ?? is this the right way to use `ch` and below?
	if err != nil {
		ch <- fmt.Errorf("failed to create listener: %v", err)
		return
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)

	go func() {
		started <- true
		if err := s.Serve(l); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		ch <- ctx.Err()
		return
	case err := <-errCh:
		ch <- err
		return
	}
}

func TunnelServerConn(ctx context.Context, ts *tunnel.Server, d client.Destination) (TunnelConn, error) {
	var tc TunnelConn

	session, err := ts.NewSession(ctx, tunnel.ServerSession{TargetID: "target1"})
	if err != nil {
		return tc, fmt.Errorf("failed to get tunnel session: %v", err)
	}

	tc.conn = session
	tc.d = &d
	return tc, nil
}

func startTunnelClient(ctx context.Context, addr string, chClient chan *tunnel.Client, chIO chan io.ReadWriteCloser, chErr chan error) {
	log.Info("starting tunnel client")
	conf := TunnelConfig{CertFile: "/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.crt",
		KeyFile: "/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.key",
		Target:  "target1"}

	opts := []grpc.DialOption{grpc.WithDefaultCallOptions()}
	if conf.CertFile == "" {
		opts = append(opts, grpc.WithInsecure())
	} else {
		creds, err := credentials.NewClientTLSFromFile(conf.CertFile, "")
		if err != nil {
			chErr <- fmt.Errorf("failed to load credentials: %v", err)
			return
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	}
	clientConn, err := grpc.Dial(addr, opts...)
	if err != nil {
		chErr <- fmt.Errorf("grpc dial error: %v", err)
		return
	}
	defer clientConn.Close()

	registerHandler := func(u string) error {
		if u != conf.Target {
			return fmt.Errorf("client cannot handle: %s", u)
		}
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
	})
	// chClient <- client
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

// TunnelConn is a wraper.
type TunnelListener struct {
	// tc   *tunnel.Client
	conn io.ReadWriteCloser
	addr tunnelAddr
	used bool
}

func (l TunnelListener) Accept() (net.Conn, error) {
	if l.conn == nil || l.used {
		return TunnelConn{}, fmt.Errorf("no connection accecpted from tunnel client")
	}
	return TunnelConn{conn: l.conn}, nil
}
func (l TunnelListener) Close() error   { return l.conn.Close() }
func (l TunnelListener) Addr() net.Addr { return l.addr }

type tunnelAddr struct {
	network string
	address string
}

func (a tunnelAddr) Network() string { return a.network }
func (a tunnelAddr) String() string  { return a.address }

func TunnelListen(ctx context.Context, addr string) (net.Listener, error) {
	// ?? how to deal with chErr?
	l := TunnelListener{}
	chErr := make(chan error)
	chClient := make(chan *tunnel.Client)
	chIO := make(chan io.ReadWriteCloser)
	for {
		if l.conn != nil {
			break
		}

		go startTunnelClient(ctx, addr, chClient, chIO, chErr)

		select {
		case err := <-chErr:
			log.Infof("failed to get tunnel listener: %v", err)
		case l.conn = <-chIO:
			l.used = false
			log.Infof("tunnel listen setup")
			break
		}
		time.Sleep(time.Second)
		log.Info("Retrying in 1 sec.")
	}
	l.addr = tunnelAddr{network: "tcp", address: addr}
	return l, nil
}
