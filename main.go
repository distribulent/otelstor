package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	pb "github.com/distribulent/otelstor/proto"
	"github.com/distribulent/otelstor/store"
)

// ---- TraceService (gRPC) -----------------------------------------------

type traceServer struct {
	collectortrace.UnimplementedTraceServiceServer
	store *store.Store
}

func (s *traceServer) Export(_ context.Context, req *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	for _, rs := range req.ResourceSpans {
		if err := s.store.WriteResourceSpans(rs); err != nil {
			log.Printf("grpc write error: %v", err)
		}
	}
	return &collectortrace.ExportTraceServiceResponse{}, nil
}

// handleHTTPExport serves POST /v1/traces for the OTLP HTTP/protobuf collector API.
func (s *traceServer) handleHTTPExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse protobuf: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, rs := range req.ResourceSpans {
		if err := s.store.WriteResourceSpans(rs); err != nil {
			log.Printf("http write error: %v", err)
		}
	}
	resp, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(resp) //nolint:errcheck
}

// ---- StatsService (gRPC) -----------------------------------------------

type statsServer struct {
	pb.UnimplementedStatsServiceServer
	store         *store.Store
	port          int
	httpPort      int
	dataDir       string
	retentionDays int
}

func (s *statsServer) GetStats(_ context.Context, _ *pb.StatsRequest) (*pb.StatsResponse, error) {
	infos, err := s.store.BucketStats()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "bucket stats: %v", err)
	}
	svcs := make([]*pb.ServiceBucket, len(infos))
	for i, info := range infos {
		months := make([]*pb.MonthBucket, len(info.Months))
		for j, m := range info.Months {
			months[j] = &pb.MonthBucket{Month: m.Month, SpanCount: m.SpanCount}
		}
		svcs[i] = &pb.ServiceBucket{Name: info.Name, Months: months}
	}
	return &pb.StatsResponse{
		Config: &pb.ServerConfig{
			Port:          int32(s.port),
			HttpPort:      int32(s.httpPort),
			DataDir:       s.dataDir,
			RetentionDays: int32(s.retentionDays),
		},
		Services: svcs,
	}, nil
}

func spanSummaryToEntry(sp store.SpanSummary) *pb.SpanEntry {
	return &pb.SpanEntry{
		TraceId:           sp.TraceID,
		SpanId:            sp.SpanID,
		ParentSpanId:      sp.ParentSpanID,
		Name:              sp.Name,
		Month:             sp.Month,
		StartTimeUnixNano: sp.StartTime.UnixNano(),
		EndTimeUnixNano:   sp.EndTime.UnixNano(),
		StatusCode:        sp.Status,
		SpanProto:         sp.SpanProto,
	}
}

func (s *statsServer) GetSpans(_ context.Context, req *pb.GetSpansRequest) (*pb.GetSpansResponse, error) {
	spans, err := s.store.GetSpans(req.GetService(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get spans: %v", err)
	}
	entries := make([]*pb.SpanEntry, len(spans))
	for i, sp := range spans {
		entries[i] = spanSummaryToEntry(sp)
	}
	return &pb.GetSpansResponse{Service: req.GetService(), Spans: entries}, nil
}

func (s *statsServer) DeleteService(_ context.Context, req *pb.DeleteServiceRequest) (*pb.DeleteServiceResponse, error) {
	if err := s.store.DeleteService(req.GetService()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete service: %v", err)
	}
	return &pb.DeleteServiceResponse{}, nil
}

func (s *statsServer) ListServices(_ context.Context, _ *pb.ListServicesRequest) (*pb.ListServicesResponse, error) {
	summaries, err := s.store.ListServices()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list services: %v", err)
	}
	svcs := make([]*pb.ServiceSummary, len(summaries))
	for i, sm := range summaries {
		svcs[i] = &pb.ServiceSummary{
			Name:                sm.Name,
			TraceCount:          sm.TraceCount,
			SpanCount:           sm.SpanCount,
			LastUpdatedUnixNano: sm.LastUpdated.UnixNano(),
		}
	}
	return &pb.ListServicesResponse{Services: svcs}, nil
}

func (s *statsServer) GetTraceIDs(_ context.Context, req *pb.GetTraceIDsRequest) (*pb.GetTraceIDsResponse, error) {
	ids, err := s.store.GetTraceIDs(req.GetService(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get trace ids: %v", err)
	}
	return &pb.GetTraceIDsResponse{Service: req.GetService(), TraceIds: ids}, nil
}

func (s *statsServer) GetTraceByID(_ context.Context, req *pb.GetTraceByIDRequest) (*pb.GetSpanTreeResponse, error) {
	spans, err := s.store.GetTraceByID(req.GetTraceId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get trace by id: %v", err)
	}
	if len(spans) == 0 {
		return nil, status.Errorf(codes.NotFound, "trace %q not found", req.GetTraceId())
	}
	entries := make([]*pb.SpanEntry, len(spans))
	for i, sp := range spans {
		entries[i] = spanSummaryToEntry(sp)
	}
	return &pb.GetSpanTreeResponse{TraceId: req.GetTraceId(), Spans: entries}, nil
}

func (s *statsServer) GetSpanTree(_ context.Context, req *pb.GetSpanTreeRequest) (*pb.GetSpanTreeResponse, error) {
	spanID, err := hex.DecodeString(req.GetSpanId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid span_id: %v", err)
	}
	traceID, spans, err := s.store.GetSpanTree(spanID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get span tree: %v", err)
	}
	if traceID == "" {
		return nil, status.Errorf(codes.NotFound, "span %q not found", req.GetSpanId())
	}
	entries := make([]*pb.SpanEntry, len(spans))
	for i, sp := range spans {
		entries[i] = spanSummaryToEntry(sp)
	}
	return &pb.GetSpanTreeResponse{TraceId: traceID, Spans: entries}, nil
}

// ---- Config & startup --------------------------------------------------

func main() {
	configFile := flag.String("config", "otelstor.cfg", "Path to textproto config file")
	flag.Parse()

	// Defaults.
	grpcPort := 4317
	httpPort := 4318
	dataDir := "./data"
	retentionDays := store.DefaultRetentionDays

	// Initialize with sentinels to detect presence without 'optional'.
	sentinelInt := int32(-1)
	rawCfg := &pb.ServerConfig{
		Port:          sentinelInt,
		HttpPort:      sentinelInt,
		RetentionDays: sentinelInt,
	}

	data, err := os.ReadFile(*configFile)
	if err != nil {
		if *configFile == "otelstor.cfg" && os.IsNotExist(err) {
			log.Printf("no config file found, using defaults")
		} else {
			log.Fatalf("load config %s: %v", *configFile, err)
		}
	} else {
		if err := prototext.Unmarshal(data, rawCfg); err != nil {
			log.Fatalf("parse config: %v", err)
		}

		if rawCfg.GetPort() != sentinelInt {
			grpcPort = int(rawCfg.GetPort())
		}
		if rawCfg.GetDataDir() != "" {
			dataDir = rawCfg.GetDataDir()
		}
		if rawCfg.GetRetentionDays() != sentinelInt {
			retentionDays = int(rawCfg.GetRetentionDays())
		}
		if rawCfg.GetHttpPort() != sentinelInt {
			httpPort = int(rawCfg.GetHttpPort())
		}
	}

	dbPath := filepath.Join(dataDir, "traces.db")
	db, err := store.Open(dbPath, retentionDays)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// Initial cleanup on startup.
	if err := db.Cleanup(); err != nil {
		log.Printf("initial cleanup error: %v", err)
	}

	tsrv := &traceServer{store: db}

	// gRPC server.
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(grpcSrv, tsrv)
	pb.RegisterStatsServiceServer(grpcSrv, &statsServer{
		store:         db,
		port:          grpcPort,
		httpPort:      httpPort,
		dataDir:       dataDir,
		retentionDays: retentionDays,
	})

	// HTTP OTLP server.
	if httpPort != 0 {
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/traces", tsrv.handleHTTPExport)
		httpLis, err := net.Listen("tcp", fmt.Sprintf(":%d", httpPort))
		if err != nil {
			log.Fatalf("http listen: %v", err)
		}
		log.Printf("OTLP HTTP listening on :%d (POST /v1/traces)", httpPort)
		go func() {
			if err := http.Serve(httpLis, mux); err != nil {
				log.Fatalf("http serve: %v", err)
			}
		}()
	} else {
		log.Printf("OTLP HTTP disabled (http_port=0)")
	}

	// Periodic cleanup of stale buckets.
	go func() {
		for {
			time.Sleep(time.Hour)
			if err := db.Cleanup(); err != nil {
				log.Printf("cleanup error: %v", err)
			}
		}
	}()

	log.Printf("OTLP gRPC listening on :%d (data=%s, retention=%dd)", grpcPort, dataDir, retentionDays)
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down")
	grpcSrv.GracefulStop()
}
