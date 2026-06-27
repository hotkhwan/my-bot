package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bottrade/internal/signals"
)

// NamedAdvisor pairs an advisor with the model name recorded in the journal, so
// the ensemble can attribute each vote to a specific model.
type NamedAdvisor struct {
	Name    string
	Advisor signals.Advisor
}

// AggregationPolicy decides how votes become a single decision.
type AggregationPolicy string

const (
	// PolicyMajority trades the side with the most votes (subject to MinVotes).
	PolicyMajority AggregationPolicy = "majority"
	// PolicyConsensus trades only when at least MinVotes advisors agree on a
	// side; otherwise it holds (no trade = capital preserved).
	PolicyConsensus AggregationPolicy = "consensus"
)

// EnsembleConfig configures an EnsembleAdvisor.
type EnsembleConfig struct {
	Advisors []NamedAdvisor
	Policy   AggregationPolicy
	// MinVotes is the minimum number of advisors that must agree on a side to
	// trade. Defaults to a simple majority of the panel.
	MinVotes int
}

// EnsembleAdvisor fans a market signal out to a panel of advisors (e.g. Claude,
// DeepSeek, Qwen) and combines their decisions. A model that errors abstains.
// When the panel does not reach the required agreement it returns ActionHold.
type EnsembleAdvisor struct {
	cfg EnsembleConfig
}

// NewEnsembleAdvisor validates config and returns the ensemble.
func NewEnsembleAdvisor(cfg EnsembleConfig) (*EnsembleAdvisor, error) {
	if len(cfg.Advisors) == 0 {
		return nil, errors.New("ensemble: at least one advisor is required")
	}
	for _, advisor := range cfg.Advisors {
		if advisor.Advisor == nil {
			return nil, errors.New("ensemble: advisor is nil")
		}
		if strings.TrimSpace(advisor.Name) == "" {
			return nil, errors.New("ensemble: advisor name is required")
		}
	}
	if cfg.Policy == "" {
		cfg.Policy = PolicyMajority
	}
	if cfg.MinVotes <= 0 {
		cfg.MinVotes = len(cfg.Advisors)/2 + 1
	}
	if cfg.Policy == PolicyConsensus && cfg.MinVotes < len(cfg.Advisors) {
		cfg.MinVotes = len(cfg.Advisors)
	}
	return &EnsembleAdvisor{cfg: cfg}, nil
}

type vote struct {
	name     string
	decision signals.Decision
}

// Decide implements signals.Advisor.
func (e *EnsembleAdvisor) Decide(ctx context.Context, signal signals.MarketSignal) (signals.Decision, error) {
	votes := make([]vote, 0, len(e.cfg.Advisors))
	for _, advisor := range e.cfg.Advisors {
		decision, err := advisor.Advisor.Decide(ctx, signal)
		if err != nil {
			// A failing model abstains rather than aborting the whole panel.
			continue
		}
		votes = append(votes, vote{name: advisor.Name, decision: decision})
	}

	bySide := map[string][]vote{}
	for _, v := range votes {
		if v.decision.Action == signals.ActionOpen && (v.decision.Side == "long" || v.decision.Side == "short") {
			bySide[v.decision.Side] = append(bySide[v.decision.Side], v)
		}
	}

	bestSide, winners := leadingSide(bySide)
	if bestSide == "" || len(winners) < e.cfg.MinVotes {
		return signals.Decision{
			Action: signals.ActionHold,
			Symbol: signal.Symbol,
			Reason: fmt.Sprintf("ensemble (%s): no agreement on a side from %d advisors", e.cfg.Policy, len(votes)),
		}, nil
	}

	// Take the levels from the highest-confidence advisor on the winning side,
	// average the confidence, and record every model that agreed.
	sort.SliceStable(winners, func(i, j int) bool {
		return winners[i].decision.ConfidencePercent > winners[j].decision.ConfidencePercent
	})
	result := winners[0].decision
	result.ConfidencePercent = averageConfidence(winners)
	result.Models = modelNames(winners)
	result.Reason = fmt.Sprintf("ensemble (%s): %d/%d agree %s [%s]",
		e.cfg.Policy, len(winners), len(votes), bestSide, strings.Join(result.Models, ", "))
	return result, nil
}

// leadingSide returns the side with the most votes. Ties resolve deterministically
// to "long" before "short" so the result is reproducible.
func leadingSide(bySide map[string][]vote) (string, []vote) {
	best := ""
	var winners []vote
	for _, side := range []string{"long", "short"} {
		if len(bySide[side]) > len(winners) {
			best = side
			winners = bySide[side]
		}
	}
	return best, winners
}

func averageConfidence(votes []vote) int {
	if len(votes) == 0 {
		return 0
	}
	total := 0
	for _, v := range votes {
		total += v.decision.ConfidencePercent
	}
	return total / len(votes)
}

func modelNames(votes []vote) []string {
	names := make([]string, 0, len(votes))
	for _, v := range votes {
		names = append(names, v.name)
	}
	return names
}
