package ai

import (
	"context"
	"errors"
	"testing"

	"bottrade/internal/signals"
)

type stubAdvisor struct {
	decision signals.Decision
	err      error
}

func (s stubAdvisor) Decide(context.Context, signals.MarketSignal) (signals.Decision, error) {
	return s.decision, s.err
}

func openVote(side string, confidence int, entry string) stubAdvisor {
	return stubAdvisor{decision: signals.Decision{
		Action:            signals.ActionOpen,
		Symbol:            "BTCUSDT",
		Side:              side,
		Entry:             entry,
		ConfidencePercent: confidence,
	}}
}

func decide(t *testing.T, cfg EnsembleConfig) signals.Decision {
	t.Helper()
	ens, err := NewEnsembleAdvisor(cfg)
	if err != nil {
		t.Fatalf("NewEnsembleAdvisor: %v", err)
	}
	d, err := ens.Decide(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return d
}

func TestEnsembleMajorityWins(t *testing.T) {
	d := decide(t, EnsembleConfig{Advisors: []NamedAdvisor{
		{Name: "claude", Advisor: openVote("long", 80, "60100")},
		{Name: "qwen", Advisor: openVote("long", 70, "60000")},
		{Name: "deepseek", Advisor: openVote("short", 60, "59000")},
	}})

	if d.Action != signals.ActionOpen || d.Side != "long" {
		t.Fatalf("decision = %+v, want open long", d)
	}
	// levels come from the highest-confidence winner (claude), confidence averaged.
	if d.Entry != "60100" {
		t.Fatalf("entry = %q, want 60100 (highest-confidence winner)", d.Entry)
	}
	if d.ConfidencePercent != 75 {
		t.Fatalf("confidence = %d, want 75 (avg of 80,70)", d.ConfidencePercent)
	}
	if len(d.Models) != 2 || d.Models[0] != "claude" || d.Models[1] != "qwen" {
		t.Fatalf("models = %v, want [claude qwen]", d.Models)
	}
}

func TestEnsembleConsensusRequiresAll(t *testing.T) {
	// 2 long / 1 short: consensus needs all 3, so it holds.
	d := decide(t, EnsembleConfig{
		Policy: PolicyConsensus,
		Advisors: []NamedAdvisor{
			{Name: "claude", Advisor: openVote("long", 80, "60100")},
			{Name: "qwen", Advisor: openVote("long", 70, "60000")},
			{Name: "deepseek", Advisor: openVote("short", 60, "59000")},
		},
	})
	if d.Action != signals.ActionHold {
		t.Fatalf("decision = %+v, want hold (no full consensus)", d)
	}
}

func TestEnsembleConsensusReached(t *testing.T) {
	d := decide(t, EnsembleConfig{
		Policy: PolicyConsensus,
		Advisors: []NamedAdvisor{
			{Name: "claude", Advisor: openVote("short", 90, "60000")},
			{Name: "qwen", Advisor: openVote("short", 80, "60050")},
		},
	})
	if d.Action != signals.ActionOpen || d.Side != "short" {
		t.Fatalf("decision = %+v, want open short", d)
	}
	if len(d.Models) != 2 {
		t.Fatalf("models = %v, want both", d.Models)
	}
}

func TestEnsembleAbstainsOnError(t *testing.T) {
	// claude errors (abstains); the two healthy long votes still carry a majority.
	d := decide(t, EnsembleConfig{Advisors: []NamedAdvisor{
		{Name: "claude", Advisor: stubAdvisor{err: errors.New("api down")}},
		{Name: "qwen", Advisor: openVote("long", 70, "60000")},
		{Name: "deepseek", Advisor: openVote("long", 65, "60010")},
	}})
	if d.Action != signals.ActionOpen || d.Side != "long" {
		t.Fatalf("decision = %+v, want open long despite one model erroring", d)
	}
	if len(d.Models) != 2 {
		t.Fatalf("models = %v, want the two healthy advisors", d.Models)
	}
}

func TestEnsembleNoOpenVotesHolds(t *testing.T) {
	d := decide(t, EnsembleConfig{Advisors: []NamedAdvisor{
		{Name: "claude", Advisor: stubAdvisor{decision: signals.Decision{Action: signals.ActionHold}}},
		{Name: "qwen", Advisor: stubAdvisor{decision: signals.Decision{Action: signals.ActionHold}}},
	}})
	if d.Action != signals.ActionHold {
		t.Fatalf("decision = %+v, want hold", d)
	}
}

func TestNewEnsembleValidates(t *testing.T) {
	if _, err := NewEnsembleAdvisor(EnsembleConfig{}); err == nil {
		t.Fatal("expected error for empty advisor list")
	}
	if _, err := NewEnsembleAdvisor(EnsembleConfig{Advisors: []NamedAdvisor{{Name: "x"}}}); err == nil {
		t.Fatal("expected error for nil advisor")
	}
}
