package service

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/scheduled"
)

func TestRunTriggersLoadedSchedulesWithoutStartupRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "investigations.yaml")
	if err := os.WriteFile(path, []byte(`scheduledInvestigations:
  - name: health
    schedule: "* * * * *"
    prompt: Check health.
    model: scheduled
  - name: capacity
    schedule: "* * * * *"
    prompt: Check capacity.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	investigations, err := scheduled.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	synctest.Test(t, func(t *testing.T) {
		requests := make(chan holmes.Request, 2)
		slack := &fakeSlack{rootTSs: []string{"100.1", "200.1"}}
		service := newService(nil, holmesFunc(func(_ context.Context, request holmes.Request) (string, error) {
			requests <- request
			return "answer", nil
		}), slack, Config{MonitoredChannel: "C1", ScheduledInvestigations: investigations})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { service.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()

		synctest.Wait()
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if len(requests) != 0 || len(slack.postLog()) != 0 {
			t.Fatal("a Scheduled Investigation ran before its next cron boundary")
		}

		time.Sleep(time.Second)
		synctest.Wait()
		if len(requests) != 2 {
			t.Fatalf("cron triggered %d investigations, want 2", len(requests))
		}
		want := map[string]struct{ ask, model string }{
			"schedule:health":   {"Check health.", "scheduled"},
			"schedule:capacity": {"Check capacity.", ""},
		}
		for range 2 {
			request := <-requests
			entry, ok := want[request.SourceRef]
			if !ok || request.Ask != entry.ask || request.Model != entry.model ||
				request.RequestSource != "scheduled_investigation" {
				t.Fatalf("scheduled Holmes request = %#v", request)
			}
			delete(want, request.SourceRef)
		}
		posts := slack.postLog()
		for _, name := range []string{"health", "capacity"} {
			if !slices.Contains(posts, "C1:Scheduled investigation started: "+name) {
				t.Fatalf("scheduled roots = %#v", posts)
			}
		}
		for _, reply := range []string{"C1:100.1:answer", "C1:200.1:answer"} {
			if !slices.Contains(slack.replyLog(), reply) {
				t.Fatalf("scheduled replies = %#v", slack.replyLog())
			}
		}
	})
}
