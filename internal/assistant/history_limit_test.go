package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/config"
)

func TestSetHistoryLimitCeiling(t *testing.T) {
	llm := &summarizeLLM{}
	svc, _ := newSummarizeService(t, llm)

	if err := svc.SetHistoryLimit(maxHistoryLimit); err != nil {
		t.Fatalf("ceiling value rejected: %v", err)
	}
	if err := svc.SetHistoryLimit(maxHistoryLimit + 1); err == nil || !strings.Contains(err.Error(), "may not exceed") {
		t.Fatalf("over-ceiling error = %v, want explicit rejection", err)
	}
	if err := svc.SetHistoryLimit(0); err == nil {
		t.Fatal("zero accepted")
	}
	_ = context.Background()
	_ = config.Config{}
}
