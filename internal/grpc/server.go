// Package grpc implements the FlowGate gRPC server.
// It exposes the same service layer (matcher, enforcer, feedback, store) as the
// REST API on a separate port. The REST server is untouched.
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/engine"
	pb "github.com/vk9551/flowgate-io/internal/grpc/pb"
	"github.com/vk9551/flowgate-io/internal/store"
)

// FlowGateGrpcServer implements the FlowGate gRPC service.
type FlowGateGrpcServer struct {
	pb.UnimplementedFlowGateServer

	cfgMu     sync.RWMutex
	cfg       *config.Config
	cfgPath   string
	store     store.Store
	startTime time.Time
}

func (s *FlowGateGrpcServer) getConfig() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *FlowGateGrpcServer) setConfig(cfg *config.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
}

// NewFlowGateGrpcServerForTest creates a FlowGateGrpcServer for use in tests
// (bypasses the TCP listener — caller registers it with an in-process server).
func NewFlowGateGrpcServerForTest(cfg *config.Config, cfgPath string, st store.Store, startTime time.Time) pb.FlowGateServer {
	return &FlowGateGrpcServer{
		cfg:       cfg,
		cfgPath:   cfgPath,
		store:     st,
		startTime: startTime,
	}
}

// StartGrpcServer starts the gRPC server on the given port and blocks.
func StartGrpcServer(port int, cfg *config.Config, cfgPath string, st store.Store, startTime time.Time) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("grpc: listen on port %d: %w", port, err)
	}

	srv := &FlowGateGrpcServer{
		cfg:       cfg,
		cfgPath:   cfgPath,
		store:     st,
		startTime: startTime,
	}

	s := grpc.NewServer()
	pb.RegisterFlowGateServer(s, srv)

	log.Printf("flowgate: gRPC listening on :%d", port)
	return s.Serve(lis)
}

// ── Health ────────────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	cfg := s.getConfig()
	uptime := int64(time.Since(s.startTime).Seconds())
	return &pb.HealthResponse{
		Status:  "ok",
		Uptime:  uptime,
		Version: cfg.Version,
	}, nil
}

// ── Evaluate ──────────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) Evaluate(_ context.Context, req *pb.EvaluateRequest) (*pb.Decision, error) {
	return s.evaluate(req)
}

// evaluate is the shared evaluation logic for Evaluate and EvaluateStream.
func (s *FlowGateGrpcServer) evaluate(req *pb.EvaluateRequest) (*pb.Decision, error) {
	cfg := s.getConfig()

	if req.SubjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "subject_id is required")
	}

	// Build event map from the request fields so MatchPriority can use its rules.
	evt := make(engine.Event)
	evt[cfg.Subject.IDField] = req.SubjectId
	if req.Type != "" {
		evt["type"] = req.Type
	}
	if req.Channel != "" {
		evt["channel"] = req.Channel
	}
	if req.SubjectTz != "" && cfg.Subject.TimezoneField != "" {
		evt[cfg.Subject.TimezoneField] = req.SubjectTz
	}
	for k, v := range req.Metadata {
		evt[k] = v
	}

	// Upsert subject, preserving existing timezone when not provided.
	subject := &store.Subject{
		ID:        req.SubjectId,
		Timezone:  req.SubjectTz,
		UpdatedAt: time.Now().UTC(),
	}
	if req.SubjectTz == "" {
		if existing, _ := s.store.SubjectGet(req.SubjectId); existing != nil {
			subject.Timezone = existing.Timezone
		}
	}
	if err := s.store.SubjectUpsert(subject); err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}

	// Match priority.
	priority := engine.MatchPriority(cfg.Priorities, evt)
	if priority == nil {
		return nil, status.Error(codes.FailedPrecondition, "no matching priority and no default configured")
	}

	// Find policy (nil → no constraints, act_now).
	policy := findPolicy(cfg, priority.Name)
	if policy == nil {
		policy = &config.Policy{Priority: priority.Name, Decision: "act_now"}
	}

	eventID := uuid.New().String()
	now := time.Now().UTC()

	decision, err := engine.CheckAndRecord(subject, priority, policy, cfg.Subject, s.store, eventID, now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "engine error: %v", err)
	}

	suppressedToday, _ := s.store.CountDecisions(req.SubjectId, engine.OutcomeSuppress, "1d")

	resp := &pb.Decision{
		Decision:        decision.Outcome,
		Reason:          decision.Reason,
		Priority:        decision.Priority,
		EventId:         eventID,
		SuppressedToday: int32(suppressedToday),
	}
	if !decision.DeliverAt.IsZero() {
		resp.DeliverAt = decision.DeliverAt.UnixMilli()
	}
	return resp, nil
}

// ── ReportOutcome ─────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) ReportOutcome(_ context.Context, req *pb.OutcomeRequest) (*pb.OutcomeResult, error) {
	cfg := s.getConfig()

	if req.EventId == "" {
		return nil, status.Error(codes.InvalidArgument, "event_id is required")
	}
	if req.Outcome == "" {
		return nil, status.Error(codes.InvalidArgument, "outcome is required")
	}

	// Validate outcome and capture refund flag before mutating state.
	outcomeCfg := findOutcomeCfg(cfg, req.Outcome)
	if outcomeCfg == nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown outcome: %s", req.Outcome)
	}

	ev, err := s.store.EventGetByID(req.EventId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}
	if ev == nil {
		return nil, status.Errorf(codes.NotFound, "event not found: %s", req.EventId)
	}
	previousOutcome := ev.Outcome

	if err := engine.ProcessOutcome(req.EventId, req.Outcome, req.Reason, "", s.store, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "feedback error: %v", err)
	}

	return &pb.OutcomeResult{
		EventId:         req.EventId,
		Outcome:         req.Outcome,
		CapRefunded:     outcomeCfg.RefundCap,
		PreviousOutcome: previousOutcome,
	}, nil
}

// ── EvaluateStream ────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) EvaluateStream(stream grpc.BidiStreamingServer[pb.EvaluateRequest, pb.Decision]) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		decision, err := s.evaluate(req)
		if err != nil {
			return err
		}
		if err := stream.Send(decision); err != nil {
			return err
		}
	}
}

// ── GetSubject ────────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) GetSubject(_ context.Context, req *pb.GetSubjectRequest) (*pb.SubjectResponse, error) {
	sub, err := s.store.SubjectGet(req.SubjectId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}
	if sub == nil {
		return nil, status.Errorf(codes.NotFound, "subject %q not found", req.SubjectId)
	}

	history, err := s.store.EventList(req.SubjectId, 20)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}

	events := make([]*pb.EventSummary, 0, len(history))
	for _, ev := range history {
		events = append(events, eventToSummary(ev))
	}

	channelHealth := sub.ChannelHealth
	if channelHealth == nil {
		channelHealth = map[string]string{}
	}

	return &pb.SubjectResponse{
		SubjectId:     sub.ID,
		Timezone:      sub.Timezone,
		ChannelHealth: channelHealth,
		RecentEvents:  events,
	}, nil
}

// ── ResetSubject ──────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) ResetSubject(_ context.Context, req *pb.ResetSubjectRequest) (*pb.ResetSubjectResponse, error) {
	if err := s.store.SubjectReset(req.SubjectId); err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}
	return &pb.ResetSubjectResponse{Status: "reset"}, nil
}

// ── GetPolicies ───────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) GetPolicies(_ context.Context, _ *pb.GetPoliciesRequest) (*pb.PoliciesResponse, error) {
	cfg := s.getConfig()
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal error: %v", err)
	}
	return &pb.PoliciesResponse{ConfigJson: string(b)}, nil
}

// ── ReloadPolicies ────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) ReloadPolicies(_ context.Context, _ *pb.ReloadPoliciesRequest) (*pb.ReloadPoliciesResponse, error) {
	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reload failed: %v", err)
	}
	s.setConfig(newCfg)
	return &pb.ReloadPoliciesResponse{
		Status:     "reloaded",
		Priorities: int32(len(newCfg.Priorities)),
	}, nil
}

// ── GetStats ──────────────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) GetStats(_ context.Context, _ *pb.GetStatsRequest) (*pb.Stats, error) {
	stats, err := s.store.StatsToday()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}

	oc := &pb.OutcomeCounts{}
	if stats.OutcomeCounts != nil {
		oc.Success = int64(stats.OutcomeCounts["success"])
		oc.FailedTemp = int64(stats.OutcomeCounts["failed_temp"])
		oc.FailedPerm = int64(stats.OutcomeCounts["failed_perm"])
		oc.Pending = int64(stats.OutcomeCounts["pending"])
	}

	return &pb.Stats{
		TotalToday:          int64(stats.TotalToday),
		SendNow:             int64(stats.ActNow),
		Delayed:             int64(stats.Delayed),
		Suppressed:          int64(stats.Suppressed),
		SuppressionRate:     stats.SuppressionRate,
		AvgDelaySeconds:     stats.AvgDelaySeconds,
		DeliverySuccessRate: stats.DeliverySuccessRate,
		OutcomeCounts:       oc,
	}, nil
}

// ── GetRecentEvents ───────────────────────────────────────────────────────────

func (s *FlowGateGrpcServer) GetRecentEvents(_ context.Context, req *pb.GetRecentEventsRequest) (*pb.RecentEventsResponse, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	events, err := s.store.EventListRecent(limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store error: %v", err)
	}

	summaries := make([]*pb.EventSummary, 0, len(events))
	for _, ev := range events {
		summaries = append(summaries, eventToSummary(ev))
	}

	return &pb.RecentEventsResponse{Events: summaries}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func findPolicy(cfg *config.Config, priorityName string) *config.Policy {
	for i := range cfg.Policies {
		if cfg.Policies[i].Priority == priorityName {
			return &cfg.Policies[i]
		}
	}
	return nil
}

func findOutcomeCfg(cfg *config.Config, name string) *config.OutcomeCfg {
	for i := range cfg.Outcomes {
		if cfg.Outcomes[i].Name == name {
			return &cfg.Outcomes[i]
		}
	}
	return nil
}

func eventToSummary(ev *store.EventRecord) *pb.EventSummary {
	return &pb.EventSummary{
		EventId:    ev.ID,
		Type:       ev.Priority,
		Decision:   ev.Decision,
		Reason:     ev.Reason,
		Outcome:    ev.Outcome,
		OccurredAt: ev.OccurredAt.UnixMilli(),
	}
}
