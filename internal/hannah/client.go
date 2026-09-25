// Package hannah keeps the collector registered with Hannah Core, so Core can
// announce it to all components via SubscribeInfrastructure.
package hannah

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Registration is what the collector announces about itself.
type Registration struct {
	Instance string
	Host     string // empty = Hannah uses the address this connection comes from
	Port     int32
	Version  string
}

// Client holds the LogCollectorConnect stream open. The collector counts as available
// exactly as long as the stream is open, so it reconnects whenever the stream drops.
type Client struct {
	hannahAddr string
	reg        Registration
	dialOpts   []grpc.DialOption
}

// New creates a client. extraDialOpts are appended to the defaults (used by tests).
func New(hannahAddr string, reg Registration, extraDialOpts ...grpc.DialOption) *Client {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(versionUnaryInterceptor),
		grpc.WithChainStreamInterceptor(versionStreamInterceptor),
	}
	return &Client{hannahAddr: hannahAddr, reg: reg, dialOpts: append(opts, extraDialOpts...)}
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
		slog.Warn("Hannah connection lost, reconnecting", "err", err, "backoff", backoff)

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

	stream, err := pb.NewHannahServiceClient(conn).LogCollectorConnect(ctx)
	if err != nil {
		return false, err
	}

	if err := stream.Send(&pb.LogCollectorMessage{
		Payload: &pb.LogCollectorMessage_Register{Register: &pb.LogCollectorRegister{
			Instance: c.reg.Instance,
			Host:     c.reg.Host,
			Port:     c.reg.Port,
			Version:  c.reg.Version,
		}},
	}); err != nil {
		return false, err
	}

	registered := false
	for {
		cmd, err := stream.Recv()
		if err != nil {
			return registered, err
		}
		switch cmd.GetCommand().(type) {
		case *pb.LogCollectorCommand_Registered:
			registered = true
			slog.Info("registered with Hannah", "addr", c.hannahAddr, "instance", c.reg.Instance)
		default:
			slog.Warn("received unknown LogCollectorCommand variant")
		}
	}
}
