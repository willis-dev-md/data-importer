// Command demo-client is a tiny gRPC client for scripts/demo.sh (used when
// grpcurl is not installed). It is not a product command.
//
//	go run ./scripts/demo-client -addr 127.0.0.1:50051 [-type month] [-min 3.5] [-size 3]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:50051", "server address (plaintext)")
	source := flag.String("source", "ons-l55o", "source id")
	ptype := flag.String("type", "", "filter period_type equals")
	min := flag.Float64("min", -1e308, "filter value >= (when set)")
	size := flag.Int("size", 3, "page size")
	flag.Parse()

	var filters []*pb.Filter
	if *ptype != "" {
		filters = append(filters, &pb.Filter{Field: "period_type", Op: pb.FilterOp_FILTER_OP_EQ, Value: structpb.NewStringValue(*ptype)})
	}
	if *min > -1e300 {
		filters = append(filters, &pb.Filter{Field: "value", Op: pb.FilterOp_FILTER_OP_GTE, Value: structpb.NewNumberValue(*min)})
	}
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo-client:", err)
		os.Exit(1)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := pb.NewDataServiceClient(conn).ListRecords(ctx,
		&pb.ListRecordsRequest{SourceId: *source, Filters: filters, PageSize: int32(*size)}, grpc.WaitForReady(true))
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo-client:", err)
		os.Exit(1)
	}
	fmt.Println(protojson.MarshalOptions{Multiline: true}.Format(resp))
}
