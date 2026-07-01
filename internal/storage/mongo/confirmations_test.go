package mongo

import (
	"testing"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
	"bottrade/internal/signals"
)

func TestConfirmationDocRoundTrip(t *testing.T) {
	createdAt := time.Unix(1710000000, 0).UTC()
	confirmation := orders.Confirmation{
		ID:     "abc123",
		UserID: 12345,
		Intent: domain.Intent{
			Type: domain.IntentOpen,
			Open: &domain.OpenIntent{
				Symbol:   "BTCUSDT",
				Side:     domain.SideLong,
				Leverage: 3,
				Entry:    decimal.MustParse("67500.50"),
				StopLoss: decimal.MustParse("65000"),
				TakeProfits: []decimal.Decimal{
					decimal.MustParse("72000"),
				},
				Size: domain.OrderSize{
					Kind:   domain.SizeUSDT,
					Amount: decimal.MustParse("100"),
				},
			},
		},
		Status:         orders.StatusPending,
		CorrelationID:  "corr123",
		IntentHash:     "hash123",
		IdempotencyKey: "confirm:abc123",
		CreatedAt:      createdAt,
		ExpiresAt:      createdAt.Add(5 * time.Minute),
	}

	doc, err := newConfirmationDoc(confirmation)
	if err != nil {
		t.Fatalf("newConfirmationDoc returned error: %v", err)
	}

	got, err := doc.toConfirmation()
	if err != nil {
		t.Fatalf("toConfirmation returned error: %v", err)
	}
	if got.ID != confirmation.ID || got.UserID != confirmation.UserID || got.Status != confirmation.Status {
		t.Fatalf("confirmation = %#v, want id/user/status preserved", got)
	}
	if got.CorrelationID != "corr123" || got.IntentHash != "hash123" || got.IdempotencyKey != "confirm:abc123" {
		t.Fatalf("confirmation metadata = %#v, want correlation/hash/idempotency preserved", got)
	}
	if got.Intent.Open == nil || got.Intent.Open.Symbol != "BTCUSDT" {
		t.Fatalf("intent = %#v, want open BTCUSDT", got.Intent)
	}
	if !got.Intent.Open.Entry.Equal(decimal.MustParse("67500.50")) {
		t.Fatalf("entry = %s, want 67500.50", got.Intent.Open.Entry.String())
	}
}

func TestTerminalConfirmationExpiryExtendsPastShortConfirmationTTL(t *testing.T) {
	now := time.Unix(1710000000, 0).UTC()
	got := terminalConfirmationExpiresAt(now)

	if !got.Equal(now.Add(terminalConfirmationRetention)) {
		t.Fatalf("terminal expiry = %s, want now + retention", got)
	}
	if !got.After(now.Add(24 * time.Hour)) {
		t.Fatalf("terminal expiry = %s, want enough retention for restart reconciliation", got)
	}
}

func TestOrderIntentDocRoundTrip(t *testing.T) {
	createdAt := time.Unix(1710000000, 0).UTC()
	record := orders.IntentRecord{
		ID:             "intent123",
		UserID:         12345,
		Intent:         testStorageOpenIntent(),
		IntentHash:     "hash123",
		CorrelationID:  "corr123",
		ConfirmationID: "confirm123",
		Status:         orders.IntentStatusAwaitingConfirmation,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}

	doc, err := newOrderIntentDoc(record)
	if err != nil {
		t.Fatalf("newOrderIntentDoc returned error: %v", err)
	}
	got, err := doc.toIntentRecord()
	if err != nil {
		t.Fatalf("toIntentRecord returned error: %v", err)
	}
	if got.ID != record.ID || got.IntentHash != record.IntentHash || got.ConfirmationID != record.ConfirmationID {
		t.Fatalf("intent record = %#v, want metadata preserved", got)
	}
	if doc.PlanID != "2" || doc.Symbol != "BTCUSDT" || doc.IntentType != string(domain.IntentOpen) {
		t.Fatalf("intent doc metadata = %#v, want plan/symbol/type", doc)
	}
	if got.Intent.Open == nil || got.Intent.Open.Symbol != "BTCUSDT" {
		t.Fatalf("intent = %#v, want open BTCUSDT", got.Intent)
	}
}

func TestSignalDocStoresSignalMetadata(t *testing.T) {
	createdAt := time.Unix(1710000000, 0).UTC()
	doc, err := newSignalDoc(signals.SignalRecord{
		Signal: signals.MarketSignal{
			Source:     "tradingview",
			Symbol:     "BTCUSDT",
			Interval:   "15",
			Price:      "67500",
			ReceivedAt: createdAt,
		},
		Decision: signals.Decision{
			Action:            signals.ActionHold,
			Symbol:            "BTCUSDT",
			ConfidencePercent: 50,
			Reason:            "wait",
		},
		Status:    signals.SignalStatusHeld,
		Message:   "Signal accepted. AI decision is hold.",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("newSignalDoc returned error: %v", err)
	}
	if doc.Symbol != "BTCUSDT" || doc.Status != signals.SignalStatusHeld {
		t.Fatalf("signal doc = %#v, want BTCUSDT held", doc)
	}
	if doc.SignalJSON == "" || doc.DecisionJSON == "" {
		t.Fatalf("signal doc JSON fields should not be empty: %#v", doc)
	}
}

func testStorageOpenIntent() domain.Intent {
	return domain.Intent{
		Type: domain.IntentOpen,
		Open: &domain.OpenIntent{
			Symbol:   "BTCUSDT",
			Side:     domain.SideLong,
			Leverage: 3,
			Entry:    decimal.MustParse("67500.50"),
			StopLoss: decimal.MustParse("65000"),
			TakeProfits: []decimal.Decimal{
				decimal.MustParse("72000"),
			},
			Size: domain.OrderSize{
				Kind:   domain.SizeUSDT,
				Amount: decimal.MustParse("100"),
			},
			PlanID: "2",
		},
	}
}
