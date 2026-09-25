package hannah

import (
	"context"
	"strconv"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ProtoVersionMetadataKey is the gRPC metadata key Hannah Core's server
// interceptor checks against its own PROTO_VERSION (core/hannah/grpc_interceptors.py).
const ProtoVersionMetadataKey = "x-proto-version"

// protoVersion is pb.ProtoVersion rendered as a string for gRPC metadata.
var protoVersion = strconv.Itoa(pb.ProtoVersion)

// versionUnaryInterceptor attaches pb.ProtoVersion to every outgoing unary call.
func versionUnaryInterceptor(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	ctx = metadata.AppendToOutgoingContext(ctx, ProtoVersionMetadataKey, protoVersion)
	return invoker(ctx, method, req, reply, cc, opts...)
}

// versionStreamInterceptor attaches pb.ProtoVersion to every outgoing streaming call
// (e.g. LogCollectorConnect).
func versionStreamInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, ProtoVersionMetadataKey, protoVersion)
	return streamer(ctx, desc, cc, method, opts...)
}
