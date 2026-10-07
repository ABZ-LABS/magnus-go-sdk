// The livecheck, checked.
//
// A livecheck that passes because it silently skipped half its checks is worse
// than no livecheck, so it is run end to end against the contract mock.
package livecheck_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ABZ-LABS/magnus-go-sdk/livecheck"
	"github.com/ABZ-LABS/magnus-go-sdk/mockmagnus"
)

func run(t *testing.T, opts livecheck.Options) (int, string) {
	t.Helper()
	var out bytes.Buffer
	opts.Out = &out
	code := livecheck.Run(context.Background(), opts)
	return code, out.String()
}

func TestAConformingServerIsACleanPass(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)

	code, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "k"})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "all 15 checks passed") {
		t.Errorf("missing the clean verdict:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a check failed:\n%s", out)
	}
}

func TestEveryCheckActuallyRuns(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)

	_, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "k"})
	for n := 1; n <= 15; n++ {
		if !strings.Contains(out, fmt.Sprintf("%2d. ", n)) {
			t.Errorf("check %d never ran:\n%s", n, out)
		}
	}
}

func TestABadKeyFailsTheGate(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.APIKey = "the_right_key"

	code, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "wrong_key"})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out, "does not meet the contract") {
		t.Errorf("missing the verdict:\n%s", out)
	}
}

func TestADeadHostFailsTheGateWithoutCrashing(t *testing.T) {
	code, out := run(t, livecheck.Options{
		BaseURL: "http://127.0.0.1:1", APIKey: "k", Timeout: 2 * time.Second,
	})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("nothing was reported as failing:\n%s", out)
	}
}

func TestAnAgentOutsideTheKeyIsNamed(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)

	code, out := run(t, livecheck.Options{
		BaseURL: server.URL(), APIKey: "k", Agent: "no_such_agent",
	})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out, "no_such_agent") {
		t.Errorf("the offending agent was not named:\n%s", out)
	}
}

// The check must fail loudly if the server drops a documented refusal.
func TestAServerThatAcceptsToolsFailsCheck11(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.UnsupportedParams = nil

	code, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "k"})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out, "documented to refuse") {
		t.Errorf("check 11 did not report the drift:\n%s", out)
	}
}

// `estimated` is a character heuristic; the report should say so.
func TestAnEstimatedUsageSourceIsCalledOut(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.UsageSource = "estimated"

	_, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "k"})
	if !strings.Contains(out, "do not bill on this") {
		t.Errorf("the heuristic was not flagged:\n%s", out)
	}
}

// Releasing UpdatesContext/FollowContext against a server that 404s it would
// break every user of the release: check 15 is what stops it.
func TestAServerWithoutTheUpdatesEndpointFailsTheGate(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.ServesNoUpdates = true

	code, out := run(t, livecheck.Options{BaseURL: server.URL(), APIKey: "k"})
	if code != 1 || !strings.Contains(out, "15. ") || !strings.Contains(out, "FAIL") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}
