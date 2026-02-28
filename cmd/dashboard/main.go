package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/distribulent/otelstor/proto"

	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

//go:embed all:ui/dist
var uiAssets embed.FS

// richSpanEntry mirrors SpanEntry but replaces the raw span_proto bytes with
// a structured JSON object decoded from the OTLP Span protobuf.
type richSpanEntry struct {
	TraceId           string          `json:"trace_id"`
	SpanId            string          `json:"span_id"`
	ParentSpanId      string          `json:"parent_span_id"`
	Name              string          `json:"name"`
	Month             string          `json:"month"`
	StartTimeUnixNano int64           `json:"start_time_unix_nano"`
	EndTimeUnixNano   int64           `json:"end_time_unix_nano"`
	StatusCode        int32           `json:"status_code"`
	SpanDetails       json.RawMessage `json:"span_details,omitempty"`
}

type richSpansResponse struct {
	Service string          `json:"service"`
	Spans   []richSpanEntry `json:"spans"`
}

type richSpanTreeResponse struct {
	TraceId string          `json:"trace_id"`
	Spans   []richSpanEntry `json:"spans"`
}

// toRichEntry decodes the SpanProto bytes into a structured JSON object using
// protojson so the frontend receives the full OTLP span without a proto library.
func toRichEntry(e *pb.SpanEntry) richSpanEntry {
	r := richSpanEntry{
		TraceId:           e.TraceId,
		SpanId:            e.SpanId,
		ParentSpanId:      e.ParentSpanId,
		Name:              e.Name,
		Month:             e.Month,
		StartTimeUnixNano: e.StartTimeUnixNano,
		EndTimeUnixNano:   e.EndTimeUnixNano,
		StatusCode:        e.StatusCode,
	}
	if len(e.SpanProto) > 0 {
		var span tracev1.Span
		if err := proto.Unmarshal(e.SpanProto, &span); err == nil {
			if b, err := protojson.Marshal(&span); err == nil {
				r.SpanDetails = json.RawMessage(b)
			}
		}
	}
	return r
}

func main() {
	grpcAddrs := flag.String("grpc-addrs", "localhost:4317", "Comma-separated list of otelstor gRPC server addresses")
	port := flag.Int("port", 10731, "HTTP port for the dashboard")
	flag.Parse()

	addrs := strings.Split(*grpcAddrs, ",")
	clients := make(map[string]pb.StatsServiceClient)
	backendList := []string{}
	var firstBackend string

	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if firstBackend == "" {
			firstBackend = addr
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("connect to gRPC %s: %v", addr, err)
		}
		defer conn.Close()
		clients[addr] = pb.NewStatsServiceClient(conn)
		backendList = append(backendList, addr)
	}

	if len(clients) == 0 {
		log.Fatalf("no valid grpc-addrs provided")
	}

	getClient := func(r *http.Request) (pb.StatsServiceClient, error) {
		backend := r.URL.Query().Get("backend")
		if backend == "" {
			return clients[firstBackend], nil
		}
		c, ok := clients[backend]
		if !ok {
			return nil, fmt.Errorf("unknown backend %q", backend)
		}
		return c, nil
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/backends", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(backendList) //nolint:errcheck
	})

	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		client, err := getClient(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := client.GetStats(context.Background(), &pb.StatsRequest{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("encode stats: %v", err)
		}
	})

	mux.HandleFunc("/api/spans", func(w http.ResponseWriter, r *http.Request) {
		client, err := getClient(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		svc := r.URL.Query().Get("service")
		if svc == "" {
			http.Error(w, "missing service parameter", http.StatusBadRequest)
			return
		}
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit) //nolint:errcheck
		}
		resp, err := client.GetSpans(context.Background(), &pb.GetSpansRequest{
			Service: svc,
			Limit:   int32(limit),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rich := richSpansResponse{Service: resp.Service, Spans: make([]richSpanEntry, len(resp.Spans))}
		for i, e := range resp.Spans {
			rich.Spans[i] = toRichEntry(e)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rich) //nolint:errcheck
	})

	mux.HandleFunc("/api/spantree", func(w http.ResponseWriter, r *http.Request) {
		client, err := getClient(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		spanID := r.URL.Query().Get("span_id")
		if spanID == "" {
			http.Error(w, "missing span_id parameter", http.StatusBadRequest)
			return
		}
		resp, err := client.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{SpanId: spanID})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rich := richSpanTreeResponse{TraceId: resp.TraceId, Spans: make([]richSpanEntry, len(resp.Spans))}
		for i, e := range resp.Spans {
			rich.Spans[i] = toRichEntry(e)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rich) //nolint:errcheck
	})

	mux.HandleFunc("/api/service", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		client, err := getClient(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		svc := r.URL.Query().Get("service")
		if svc == "" {
			http.Error(w, "missing service parameter", http.StatusBadRequest)
			return
		}
		_, err = client.DeleteService(context.Background(), &pb.DeleteServiceRequest{Service: svc})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	uiFS, err := fs.Sub(uiAssets, "ui/dist")
	if err != nil {
		log.Fatalf("failed to create sub filesystem: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(uiFS)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("dashboard listening on %s (grpc-addrs=%s)", addr, *grpcAddrs)
	log.Fatal(http.ListenAndServe(addr, mux))
}
