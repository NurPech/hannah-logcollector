// Package hannah keeps the collector registered with Hannah Core, so Core can
// announce it to all components via SubscribeInfrastructure.
package hannah

import (
	"context"
	"log/slog"
	"time"

	v1pb "github.com/NurPech/hannah-proto-go/v5/hannahv1"
	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"gitlab.com/gessinger/hannah-grpc-lib/go/client"
	"google.golang.org/grpc"
)

// Registration is what the collector announces about itself.
type Registration struct {
	Instance string
	Host     string // empty = Hannah uses the address this connection comes from
	Port     int32
	Version  string
	// SyslogPort is the UDP port of the syslog receiver on Host, 0 = none. Only hannah.v2 can
	// carry it; against a Core on hannah.v1 it stays unannounced.
	SyslogPort int32
}

// Client holds the LogCollectorConnect stream open. The collector counts as available
// exactly as long as the stream is open, so it reconnects whenever the stream drops.
type Client struct {
	hannahAddr string
	reg        Registration
	dialOpts   []grpc.DialOption
}

// New creates a client. extraDialOpts are appended to the defaults (used by tests).
// The defaults attach x-proto-version and x-compat-version (hannah-grpc-lib).
func New(hannahAddr string, reg Registration, extraDialOpts ...grpc.DialOption) *Client {
	return &Client{hannahAddr: hannahAddr, reg: reg, dialOpts: append(client.DialOptions(), extraDialOpts...)}
}

// Run registers with Hannah and re-registers on failure. Blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		registered, err := c.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if registered {
			backoff = time.Second // the connection worked — start over with a short delay
		}
		slog.Warn("Hannah connection lost, reconnecting", "addr", c.hannahAddr, "err", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if !registered {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// connect opens LogCollectorConnect, registers, and waits until the stream ends.
// Reports whether Hannah acknowledged the registration.
func (c *Client) connect(ctx context.Context) (bool, error) {
	conn, err := grpc.NewClient(c.hannahAddr, c.dialOpts...)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	// hannah.v2, falling back to hannah.v1 (N−1) when Core predates it, each with its own
	// messages. A new connection per attempt means the probe runs again after every reconnect.
	var stream connectStream
	if client.New(conn, nil).Resolve(ctx) == client.V1 {
		st, err := v1pb.NewHannahServiceClient(conn).LogCollectorConnect(ctx)
		if err != nil {
			return false, err
		}
		stream = v1Stream{st}
	} else {
		st, err := pb.NewHannahServiceClient(conn).LogCollectorConnect(ctx)
		if err != nil {
			return false, err
		}
		stream = v2Stream{st}
	}

	if err := stream.register(c.reg); err != nil {
		return false, err
	}

	registered := false
	for {
		ack, err := stream.recv()
		if err != nil {
			return registered, err
		}
		if !ack {
			slog.Warn("received unknown LogCollectorCommand variant")
			continue
		}
		registered = true
		slog.Info("registered with Hannah", "addr", c.hannahAddr, "instance", c.reg.Instance)
	}
}

// connectStream is a LogCollectorConnect stream of either generation.
type connectStream interface {
	register(Registration) error
	// recv reports whether the received command was the acknowledgement of the registration.
	recv() (bool, error)
}

type v2Stream struct {
	grpc.BidiStreamingClient[pb.LogCollectorMessage, pb.LogCollectorCommand]
}

func (s v2Stream) register(r Registration) error {
	return s.Send(&pb.LogCollectorMessage{
		Payload: &pb.LogCollectorMessage_Register{Register: &pb.LogCollectorRegister{
			Instance: r.Instance, Host: r.Host, Port: r.Port, Version: r.Version, SyslogPort: r.SyslogPort,
		}},
	})
}

func (s v2Stream) recv() (bool, error) {
	cmd, err := s.Recv()
	if err != nil {
		return false, err
	}
	_, ok := cmd.GetCommand().(*pb.LogCollectorCommand_Registered)
	return ok, nil
}

type v1Stream struct {
	grpc.BidiStreamingClient[v1pb.LogCollectorMessage, v1pb.LogCollectorCommand]
}

func (s v1Stream) register(r Registration) error {
	return s.Send(&v1pb.LogCollectorMessage{
		Payload: &v1pb.LogCollectorMessage_Register{Register: &v1pb.LogCollectorRegister{
			Instance: r.Instance, Host: r.Host, Port: r.Port, Version: r.Version,
		}},
	})
}

func (s v1Stream) recv() (bool, error) {
	cmd, err := s.Recv()
	if err != nil {
		return false, err
	}
	_, ok := cmd.GetCommand().(*v1pb.LogCollectorCommand_Registered)
	return ok, nil
}
