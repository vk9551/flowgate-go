package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/vk9551/flowgate-io/internal/config"
	flowgategrpc "github.com/vk9551/flowgate-io/internal/grpc"
	pb "github.com/vk9551/flowgate-io/internal/grpc/pb"
	"github.com/vk9551/flowgate-io/internal/store"
)

const bufSize = 1024 * 1024

// testServer starts an in-process gRPC server using bufconn and returns a client.
func testServer(t *testing.T) (pb.FlowGateClient, func()) {
	t.Helper()

	cfg := &config.Config{
		Version: "1.0",
		Subject: config.SubjectCfg{
			IDField: "user_id",
		},
		Priorities: []config.Priority{
			{Name: "bypass", BypassAll: true, Match: []config.MatchRule{
				{Field: "type", In: []string{"otp"}},
			}},
			{Name: "normal", Default: true},
		},
		Policies: []config.Policy{
			{Priority: "bypass", Decision: "act_now"},
			{
				Priority: "normal",
				Decision: "act_now",
				Caps: []config.CapRule{
					{Scope: "subject", PeriodRaw: "1d", Limit: 1},
				},
				DecisionOnCapBreach: "suppress",
			},
		},
		Outcomes: []config.OutcomeCfg{
			{Name: "success", Terminal: true},
			{Name: "failed_temp", RefundCap: true},
			{Name: "pending"},
		},
		DefaultOutcome: "pending",
	}

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	lis := bufconn.Listen(bufSize)

	srv := grpc.NewServer()
	// Register via the internal server builder exposed for tests.
	pb.RegisterFlowGateServer(srv, flowgategrpc.NewFlowGateGrpcServerForTest(cfg, "test.yaml", st, time.Now()))

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("grpc serve: %v", err)
		}
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}

	cleanup := func() {
		conn.Close()
		srv.Stop()
		st.Close()
	}
	return pb.NewFlowGateClient(conn), cleanup
}

// T1: Health → status "ok"
func TestHealth(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	resp, err := client.Health(context.Background(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("want status=ok, got %q", resp.Status)
	}
}

// T2: Evaluate with bypass_all priority → ACT_NOW
func TestEvaluateBypassAll(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	resp, err := client.Evaluate(context.Background(), &pb.EvaluateRequest{
		SubjectId: "user-1",
		Metadata:  map[string]string{"type": "otp"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if resp.Decision != "ACT_NOW" {
		t.Errorf("want ACT_NOW, got %q", resp.Decision)
	}
	if resp.Reason != "bypass_all" {
		t.Errorf("want reason=bypass_all, got %q", resp.Reason)
	}
}

// T3: Evaluate until cap breach → SUPPRESS
func TestEvaluateCapBreach(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	// First call → ACT_NOW (cap at 1/day, not yet breached)
	_, err := client.Evaluate(context.Background(), &pb.EvaluateRequest{
		SubjectId: "user-cap",
	})
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}

	// Second call → SUPPRESS (cap breached)
	resp, err := client.Evaluate(context.Background(), &pb.EvaluateRequest{
		SubjectId: "user-cap",
	})
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if resp.Decision != "SUPPRESS" {
		t.Errorf("want SUPPRESS after cap breach, got %q", resp.Decision)
	}
}

// T4: ReportOutcome failed_temp → cap_refunded: true
func TestReportOutcomeFailedTemp(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	evResp, err := client.Evaluate(context.Background(), &pb.EvaluateRequest{
		SubjectId: "user-outcome",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	outResp, err := client.ReportOutcome(context.Background(), &pb.OutcomeRequest{
		EventId: evResp.EventId,
		Outcome: "failed_temp",
	})
	if err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if !outResp.CapRefunded {
		t.Error("want cap_refunded=true for failed_temp")
	}
}

// T5: EvaluateStream — send 3 events, receive 3 decisions
func TestEvaluateStream(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	stream, err := client.EvaluateStream(context.Background())
	if err != nil {
		t.Fatalf("EvaluateStream: %v", err)
	}

	subjects := []string{"stream-1", "stream-2", "stream-3"}
	for _, sid := range subjects {
		if err := stream.Send(&pb.EvaluateRequest{SubjectId: sid}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var decisions []string
	for {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		decisions = append(decisions, resp.Decision)
	}

	if len(decisions) != 3 {
		t.Errorf("want 3 decisions, got %d", len(decisions))
	}
}
