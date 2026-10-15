package validate

import (
	"testing"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveCountsStatusAndChecks(t *testing.T) {
	failedBefore := testutil.ToFloat64(validations.WithLabelValues("failed"))
	testsFailedBefore := testutil.ToFloat64(checks.WithLabelValues("tests", "failed"))

	observe(events.ValidationCompletedPayload{
		Status:         "failed",
		Tests:          events.CheckResult{Status: "failed"},
		Security:       events.CheckResult{Status: "passed"},
		StaticAnalysis: events.CheckResult{Status: "skipped"},
	}, 2*time.Second)

	if got := testutil.ToFloat64(validations.WithLabelValues("failed")); got != failedBefore+1 {
		t.Errorf("validations{failed} = %v", got)
	}
	if got := testutil.ToFloat64(checks.WithLabelValues("tests", "failed")); got != testsFailedBefore+1 {
		t.Errorf("checks{tests,failed} = %v", got)
	}
}
