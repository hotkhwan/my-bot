package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newStubBinance(t *testing.T) *BinanceProvider {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("premiumIndex symbol = %q", r.URL.Query().Get("symbol"))
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"68000.0","indexPrice":"67990.0","lastFundingRate":"0.0001","nextFundingTime":1700000000000,"time":1699999000000}`))
	})
	mux.HandleFunc("/fapi/v1/openInterest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","openInterest":"12345.67","time":1699999000000}`))
	})
	mux.HandleFunc("/futures/data/globalLongShortAccountRatio", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("period") != "1h" {
			t.Errorf("ratio period = %q, want 1h", r.URL.Query().Get("period"))
		}
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","longShortRatio":"1.8","longAccount":"0.64","shortAccount":"0.36","timestamp":1699999000000}]`))
	})
	mux.HandleFunc("/futures/data/takerlongshortRatio", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"buySellRatio":"1.2","buyVol":"1000","sellVol":"833","timestamp":1699999000000}]`))
	})
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, _ *http.Request) {
		// [openTime, open, high, low, close, volume, ...]
		_, _ = w.Write([]byte(`[[1,"10","11","9","10.5","100",2],[2,"10.5","12","10","11.0","120",3],[3,"11","13","10","12.25","150",4]]`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewBinanceProvider(server.URL, server.Client())
}

func TestBinanceProviderFunding(t *testing.T) {
	funding, err := newStubBinance(t).Funding(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("Funding: %v", err)
	}
	if funding.MarkPrice.String() != "68000" || funding.LastFundingRate.String() != "0.0001" {
		t.Fatalf("funding = %+v", funding)
	}
	if funding.NextFundingTime.IsZero() {
		t.Fatal("next funding time not parsed")
	}
}

func TestBinanceProviderOpenInterest(t *testing.T) {
	oi, err := newStubBinance(t).OpenInterest(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if oi.OpenInterest.String() != "12345.67" {
		t.Fatalf("open interest = %s", oi.OpenInterest.String())
	}
}

func TestBinanceProviderLongShortRatio(t *testing.T) {
	ls, err := newStubBinance(t).LongShortRatio(context.Background(), "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("LongShortRatio: %v", err)
	}
	if ls.Ratio.String() != "1.8" || ls.LongAccount.String() != "0.64" || ls.Period != "1h" {
		t.Fatalf("long/short = %+v", ls)
	}
}

func TestBinanceProviderTakerFlow(t *testing.T) {
	taker, err := newStubBinance(t).TakerFlow(context.Background(), "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("TakerFlow: %v", err)
	}
	if taker.BuySellRatio.String() != "1.2" || taker.BuyVolume.String() != "1000" {
		t.Fatalf("taker = %+v", taker)
	}
}

func TestBinanceProviderCollectAggregates(t *testing.T) {
	snap, err := Collect(context.Background(), newStubBinance(t), "BTCUSDT", "1h", testTime())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snap.Funding.MarkPrice.String() != "68000" ||
		snap.OpenInterest.OpenInterest.String() != "12345.67" ||
		snap.LongShort.Ratio.String() != "1.8" ||
		snap.Taker.BuySellRatio.String() != "1.2" {
		t.Fatalf("snapshot incomplete: %+v", snap)
	}
}

func TestBinanceProviderCloses(t *testing.T) {
	closes, err := newStubBinance(t).Closes(context.Background(), "BTCUSDT", "1h", 200)
	if err != nil {
		t.Fatalf("Closes: %v", err)
	}
	want := []float64{10.5, 11.0, 12.25}
	if len(closes) != len(want) {
		t.Fatalf("closes = %v, want %v", closes, want)
	}
	for i := range want {
		if closes[i] != want[i] {
			t.Fatalf("closes[%d] = %v, want %v", i, closes[i], want[i])
		}
	}
}

func TestBinanceProviderInvalidPeriodDefaultsTo5m(t *testing.T) {
	if got := validPeriod("90m"); got != "5m" {
		t.Fatalf("validPeriod(90m) = %q, want 5m", got)
	}
	if got := validPeriod("4h"); got != "4h" {
		t.Fatalf("validPeriod(4h) = %q, want 4h", got)
	}
}

func TestBinanceProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	_, err := NewBinanceProvider(server.URL, server.Client()).OpenInterest(context.Background(), "NOPE")
	if err == nil {
		t.Fatal("expected an error on HTTP 400")
	}
}
