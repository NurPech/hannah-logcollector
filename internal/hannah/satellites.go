package hannah

import (
	"context"
	"time"

	v1pb "github.com/NurPech/hannah-proto-go/v5/hannahv1"
	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"gitlab.com/gessinger/hannah-grpc-lib/go/client"
	"google.golang.org/grpc"
)

// satellitesTimeout bounds one GetSatellites call including the connection to Core.
const satellitesTimeout = 10 * time.Second

// Satellites asks Hannah Core which satellites it knows.
type Satellites struct {
	hannahAddr string
	dialOpts   []grpc.DialOption
}

// NewSatellites creates the client. extraDialOpts are appended to the defaults (used by tests).
func NewSatellites(hannahAddr string, extraDialOpts ...grpc.DialOption) *Satellites {
	return &Satellites{hannahAddr: hannahAddr, dialOpts: append(client.DialOptions(), extraDialOpts...)}
}

// Names returns the display name of every satellite that has one, by device ID. It speaks
// hannah.v2 and falls back to hannah.v1 when Core predates it, like the registration does.
func (s *Satellites) Names(ctx context.Context) (map[string]string, error) {
	conn, err := grpc.NewClient(s.hannahAddr, s.dialOpts...)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(ctx, satellitesTimeout)
	defer cancel()

	names := map[string]string{}
	if client.New(conn, nil).Resolve(ctx) == client.V1 {
		resp, err := v1pb.NewHannahServiceClient(conn).GetSatellites(ctx, &v1pb.Empty{})
		if err != nil {
			return nil, err
		}
		for _, sat := range resp.GetSatellites() {
			if sat.GetDeviceId() != "" && sat.GetDisplayName() != "" {
				names[sat.GetDeviceId()] = sat.GetDisplayName()
			}
		}
		return names, nil
	}
	resp, err := pb.NewHannahServiceClient(conn).GetSatellites(ctx, &pb.Empty{})
	if err != nil {
		return nil, err
	}
	for _, sat := range resp.GetSatellites() {
		if sat.GetDeviceId() != "" && sat.GetDisplayName() != "" {
			names[sat.GetDeviceId()] = sat.GetDisplayName()
		}
	}
	return names, nil
}
