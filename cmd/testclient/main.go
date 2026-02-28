// testclient generates synthetic OTLP trace spans and sends them to an otelstor gRPC server.
// With -dump it retrieves and prints the last 50 stored entries for the given service instead.
// With -traces it lists the last 100 unique trace IDs for the given service.
// With -services it lists all services with trace count, span count, and last-updated time.
// With -trace <hex-id> it fetches all spans for that trace and prints them as an indented tree.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	mathrand "math/rand"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	pb "github.com/distribulent/otelstor/proto"
)

var (
	spanNames = []string{
		"GET /api/users",
		"POST /api/orders",
		"GET /api/products",
		"PUT /api/users/{id}",
		"DELETE /api/sessions",
		"GET /health",
		"POST /api/payments",
		"GET /api/inventory",
	}

	spanKinds = []tracev1.Span_SpanKind{
		tracev1.Span_SPAN_KIND_SERVER,
		tracev1.Span_SPAN_KIND_CLIENT,
		tracev1.Span_SPAN_KIND_INTERNAL,
	}

	statusNames = map[int32]string{
		0: "UNSET",
		1: "OK",
		2: "ERROR",
	}
)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("rand: %v", err)
	}
	return b
}

func main() {
	addr     := flag.String("addr", "localhost:4317", "otelstor gRPC server address")
	service  := flag.String("service", "frontend", "Service name")
	count    := flag.Int("count", 100, "Number of spans to send (send mode)")
	dump     := flag.Bool("dump", false, "Dump the last 50 stored entries for -service instead of sending")
	traces   := flag.Bool("traces", false, "List the last 100 unique trace IDs for -service instead of sending")
	services := flag.Bool("services", false, "List all services with trace count, span count, and last-updated time")
	traceID  := flag.String("trace", "", "Hex trace ID to fetch and display as a span tree")
	flag.Parse()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if *dump {
		dumpSpans(conn, *service)
		return
	}
	if *traces {
		listTraceIDs(conn, *service)
		return
	}
	if *services {
		listServices(conn)
		return
	}
	if *traceID != "" {
		showTrace(conn, *traceID)
		return
	}

	sendSpans(conn, *service, *count, *addr)
}

func showTrace(conn *grpc.ClientConn, traceID string) {
	client := pb.NewStatsServiceClient(conn)
	resp, err := client.GetTraceByID(context.Background(), &pb.GetTraceByIDRequest{
		TraceId: traceID,
	})
	if err != nil {
		log.Fatalf("GetTraceByID: %v", err)
	}

	spans := resp.GetSpans()
	fmt.Printf("Trace %s — %d span(s)\n\n", traceID, len(spans))
	if len(spans) == 0 {
		fmt.Println("  (no spans found)")
		return
	}

	// Build parent→children map.
	children := make(map[string][]*pb.SpanEntry)
	var roots []*pb.SpanEntry
	for _, sp := range spans {
		if sp.ParentSpanId == "" {
			roots = append(roots, sp)
		} else {
			children[sp.ParentSpanId] = append(children[sp.ParentSpanId], sp)
		}
	}

	byStart := func(s []*pb.SpanEntry) {
		sort.Slice(s, func(i, j int) bool {
			return s[i].StartTimeUnixNano < s[j].StartTimeUnixNano
		})
	}
	byStart(roots)
	for _, clist := range children {
		byStart(clist)
	}

	// Find the earliest start across all spans to use as trace-relative offset.
	traceStart := spans[0].StartTimeUnixNano
	for _, sp := range spans[1:] {
		if sp.StartTimeUnixNano < traceStart {
			traceStart = sp.StartTimeUnixNano
		}
	}

	fmt.Printf("  %-48s  %-18s  %8s  %8s  %s\n", "SPAN", "SPAN ID", "OFFSET", "DURATION", "STATUS")
	fmt.Printf("  %-48s  %-18s  %8s  %8s  %s\n", strings.Repeat("-", 48), strings.Repeat("-", 18), "------", "--------", "------")

	var printSpan func(sp *pb.SpanEntry, depth int)
	printSpan = func(sp *pb.SpanEntry, depth int) {
		indent := strings.Repeat("  ", depth)
		label := indent + sp.Name
		if len(label) > 48 {
			label = label[:45] + "..."
		}
		sid := sp.SpanId
		if len(sid) > 18 {
			sid = sid[:17] + "…"
		}
		offset := time.Duration(sp.StartTimeUnixNano - traceStart)
		dur    := time.Duration(sp.EndTimeUnixNano - sp.StartTimeUnixNano)
		status := statusNames[sp.StatusCode]
		if status == "" {
			status = fmt.Sprintf("code=%d", sp.StatusCode)
		}
		fmt.Printf("  %-48s  %-18s  %8s  %8s  %s\n",
			label, sid, formatDur(offset), formatDur(dur), status)
		for _, child := range children[sp.SpanId] {
			printSpan(child, depth+1)
		}
	}

	for _, root := range roots {
		printSpan(root, 0)
	}
}

func listServices(conn *grpc.ClientConn) {
	client := pb.NewStatsServiceClient(conn)
	resp, err := client.ListServices(context.Background(), &pb.ListServicesRequest{})
	if err != nil {
		log.Fatalf("ListServices: %v", err)
	}

	svcs := resp.GetServices()
	fmt.Printf("%d service(s)\n\n", len(svcs))
	if len(svcs) == 0 {
		fmt.Println("  (no services stored)")
		return
	}

	fmt.Printf("  %-30s  %8s  %8s  %s\n", "SERVICE", "TRACES", "SPANS", "LAST UPDATED")
	fmt.Printf("  %-30s  %8s  %8s  %s\n", "-------", "------", "-----", "------------")
	for _, svc := range svcs {
		updated := "(none)"
		if svc.LastUpdatedUnixNano > 0 {
			updated = time.Unix(0, svc.LastUpdatedUnixNano).UTC().Format("2006-01-02 15:04:05 UTC")
		}
		fmt.Printf("  %-30s  %8d  %8d  %s\n", svc.Name, svc.TraceCount, svc.SpanCount, updated)
	}
}

func listTraceIDs(conn *grpc.ClientConn, service string) {
	client := pb.NewStatsServiceClient(conn)
	resp, err := client.GetTraceIDs(context.Background(), &pb.GetTraceIDsRequest{
		Service: service,
		Limit:   100,
	})
	if err != nil {
		log.Fatalf("GetTraceIDs: %v", err)
	}

	ids := resp.GetTraceIds()
	fmt.Printf("Service %q — %d unique trace IDs (newest first)\n\n", service, len(ids))
	if len(ids) == 0 {
		fmt.Println("  (no traces stored)")
		return
	}
	for i, id := range ids {
		fmt.Printf("  %3d  %s\n", i+1, id)
	}
}

func dumpSpans(conn *grpc.ClientConn, service string) {
	client := pb.NewStatsServiceClient(conn)
	resp, err := client.GetSpans(context.Background(), &pb.GetSpansRequest{
		Service: service,
		Limit:   50,
	})
	if err != nil {
		log.Fatalf("GetSpans: %v", err)
	}

	spans := resp.GetSpans()
	fmt.Printf("Service %q — %d entries (newest first)\n\n", service, len(spans))
	if len(spans) == 0 {
		fmt.Println("  (no spans stored)")
		return
	}

	for i, sp := range spans {
		start := time.Unix(0, sp.StartTimeUnixNano).UTC()
		dur   := time.Duration(sp.EndTimeUnixNano - sp.StartTimeUnixNano)
		tid   := sp.TraceId
		if len(tid) > 16 {
			tid = tid[:16] + "…"
		}
		statusStr := statusNames[sp.StatusCode]
		if statusStr == "" {
			statusStr = fmt.Sprintf("code=%d", sp.StatusCode)
		}
		fmt.Printf("  %3d  %-30s  trace=%-17s  span=%-17s  %s  %s  %s\n",
			i+1,
			sp.Name,
			tid,
			sp.SpanId[:min(16, len(sp.SpanId))]+"…",
			start.Format("2006-01-02 15:04:05"),
			formatDur(dur),
			statusStr,
		)
	}
}

func sendSpans(conn *grpc.ClientConn, service string, count int, addr string) {
	client := collectortrace.NewTraceServiceClient(conn)

	resource := &resourcev1.Resource{
		Attributes: []*commonv1.KeyValue{
			{
				Key: "service.name",
				Value: &commonv1.AnyValue{
					Value: &commonv1.AnyValue_StringValue{StringValue: service},
				},
			},
		},
	}

	scope := &commonv1.InstrumentationScope{
		Name:    "otelstor-testclient",
		Version: "1.0.0",
	}

	sent, failed := 0, 0
	for i := 0; i < count; i++ {
		startTime := time.Now().Add(-time.Duration(mathrand.Int63n(int64(time.Hour))))
		endTime   := startTime.Add(time.Duration(mathrand.Int63n(int64(2 * time.Second))))

		req := &collectortrace.ExportTraceServiceRequest{
			ResourceSpans: []*tracev1.ResourceSpans{{
				Resource: resource,
				ScopeSpans: []*tracev1.ScopeSpans{{
					Scope: scope,
					Spans: []*tracev1.Span{{
						TraceId:           randomBytes(16),
						SpanId:            randomBytes(8),
						Name:              spanNames[i%len(spanNames)],
						Kind:              spanKinds[i%len(spanKinds)],
						StartTimeUnixNano: uint64(startTime.UnixNano()),
						EndTimeUnixNano:   uint64(endTime.UnixNano()),
						Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK},
					}},
				}},
			}},
		}

		if _, err := client.Export(context.Background(), req); err != nil {
			log.Printf("span %d: export failed: %v", i+1, err)
			failed++
			continue
		}
		sent++
	}

	fmt.Printf("sent %d/%d spans  service=%q  addr=%s\n", sent, count, service, addr)
	if failed > 0 {
		fmt.Printf("failed: %d\n", failed)
	}
}

func formatDur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
