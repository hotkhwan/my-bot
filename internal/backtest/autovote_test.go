package backtest

import "testing"

type fixedSignal struct{ s Signal }

func (f fixedSignal) Name() string             { return "fixed" }
func (f fixedSignal) Evaluate([]float64) Signal { return f.s }

func TestAutoVoteStrategy(t *testing.T) {
	cases := []struct {
		name    string
		members []Strategy
		want    Signal
	}{
		{"majority long", []Strategy{fixedSignal{Long}, fixedSignal{Long}, fixedSignal{Short}}, Long},
		{"majority short", []Strategy{fixedSignal{Short}, fixedSignal{Short}, fixedSignal{Flat}}, Short},
		{"tie is flat", []Strategy{fixedSignal{Long}, fixedSignal{Short}}, Flat},
		{"all flat", []Strategy{fixedSignal{Flat}, fixedSignal{Flat}}, Flat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (AutoVoteStrategy{Members: c.members}).Evaluate([]float64{1, 2, 3}); got != c.want {
				t.Fatalf("vote = %v, want %v", got, c.want)
			}
		})
	}

	// DefaultEnsemble is wired with the five public strategies and is usable.
	if n := len(DefaultEnsemble().Members); n != 5 {
		t.Fatalf("DefaultEnsemble members = %d, want 5", n)
	}
	if DefaultEnsemble().Name() != "auto" {
		t.Fatalf("ensemble name = %q, want auto", DefaultEnsemble().Name())
	}
}
