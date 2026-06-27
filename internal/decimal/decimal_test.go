package decimal

import (
	"encoding/json"
	"testing"
)

func TestParseStringNormalizesPlainDecimals(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "67500", want: "67500"},
		{input: "3300.5000", want: "3300.5"},
		{input: ".05", want: "0.05"},
		{input: "000.0100", want: "0.01"},
		{input: "-1.2300", want: "-1.23"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}

			if got.String() != tt.want {
				t.Fatalf("String = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestParseRejectsInvalidDecimals(t *testing.T) {
	for _, input := range []string{"", ".", "1.2.3", "12a", "-"} {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse(input); err == nil {
				t.Fatal("Parse returned nil error, want error")
			}
		})
	}
}

func TestCmpAlignsDifferentScales(t *testing.T) {
	if MustParse("1.20").Cmp(MustParse("1.2")) != 0 {
		t.Fatal("1.20 should compare equal to 1.2")
	}
	if MustParse("1.21").Cmp(MustParse("1.2")) <= 0 {
		t.Fatal("1.21 should compare greater than 1.2")
	}
	if MustParse("1.19").Cmp(MustParse("1.2")) >= 0 {
		t.Fatal("1.19 should compare less than 1.2")
	}
}

func TestMulQuoFloorAndFloorToStep(t *testing.T) {
	if got := MustParse("0.05").Mul(MustParse("67500")).String(); got != "3375" {
		t.Fatalf("Mul = %q, want 3375", got)
	}

	quotient, err := MustParse("100").QuoFloor(MustParse("67500"), 8)
	if err != nil {
		t.Fatalf("QuoFloor returned error: %v", err)
	}
	if got := quotient.String(); got != "0.00148148" {
		t.Fatalf("QuoFloor = %q, want 0.00148148", got)
	}

	floored, err := quotient.FloorToStep(MustParse("0.001"))
	if err != nil {
		t.Fatalf("FloorToStep returned error: %v", err)
	}
	if got := floored.String(); got != "0.001" {
		t.Fatalf("FloorToStep = %q, want 0.001", got)
	}
}

func TestAddSub(t *testing.T) {
	cases := []struct {
		name     string
		a, b     string
		add, sub string
	}{
		{"same scale", "1.20", "0.30", "1.5", "0.9"},
		{"different scale", "1.2", "0.05", "1.25", "1.15"},
		{"cross zero", "0.9", "1.1", "2", "-0.2"},
		{"negatives", "-5", "-2.5", "-7.5", "-2.5"},
		{"to zero", "100.00", "100", "200", "0"},
		{"pnl sum", "0.9", "1.2", "2.1", "-0.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MustParse(tc.a).Add(MustParse(tc.b)).String(); got != tc.add {
				t.Fatalf("%s + %s = %q, want %q", tc.a, tc.b, got, tc.add)
			}
			if got := MustParse(tc.a).Sub(MustParse(tc.b)).String(); got != tc.sub {
				t.Fatalf("%s - %s = %q, want %q", tc.a, tc.b, got, tc.sub)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(MustParse("1.2300"))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if string(data) != `"1.23"` {
		t.Fatalf("json = %s, want quoted normalized decimal", data)
	}

	var got Decimal
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if got.String() != "1.23" {
		t.Fatalf("decimal = %q, want 1.23", got.String())
	}
}
