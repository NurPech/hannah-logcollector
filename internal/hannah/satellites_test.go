package hannah

import (
	"context"
	"testing"
	"time"

	v1pb "github.com/NurPech/hannah-proto-go/v5/hannahv1"
	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// satelliteCore is a Core that serves hannah.v2 and knows three satellites, one without a name.
type satelliteCore struct {
	pb.UnimplementedHannahServiceServer
}

func (satelliteCore) GetSatellites(context.Context, *pb.Empty) (*pb.GetSatellitesResponse, error) {
	return &pb.GetSatellitesResponse{Satellites: []*pb.Satellite{
		{DeviceId: "9070690de440", DisplayName: "Test02"},
		{DeviceId: "e072a1d01ac8", DisplayName: "OGZimmerSüd01"},
		{DeviceId: "e072a1d01adc"},
	}}, nil
}

// satelliteCoreV1 is a Core too old for hannah.v2.
type satelliteCoreV1 struct {
	v1pb.UnimplementedHannahServiceServer
}

func (satelliteCoreV1) GetSatellites(context.Context, *v1pb.Empty) (*v1pb.GetSatellitesResponse, error) {
	return &v1pb.GetSatellitesResponse{Satellites: []*v1pb.Satellite{
		{DeviceId: "9070690de440", DisplayName: "Test02"},
		{DeviceId: "e072a1d01adc"},
	}}, nil
}

func TestNamesOfTheSatellites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(*grpc.Server)
		want     map[string]string
	}{
		{"hannah.v2", func(s *grpc.Server) { pb.RegisterHannahServiceServer(s, satelliteCore{}) },
			map[string]string{"9070690de440": "Test02", "e072a1d01ac8": "OGZimmerSüd01"}},
		{"Core without hannah.v2", func(s *grpc.Server) { v1pb.RegisterHannahServiceServer(s, satelliteCoreV1{}) },
			map[string]string{"9070690de440": "Test02"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialer := startFake(t, tc.register)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := NewSatellites("passthrough:///bufnet", dialer).Names(ctx)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "a satellite without a display name is left out")
		})
	}
}

func TestNamesFailWhenCoreIsNotReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := NewSatellites("127.0.0.1:1").Names(ctx)

	assert.Error(t, err)
}
