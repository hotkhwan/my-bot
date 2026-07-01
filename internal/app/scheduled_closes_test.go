package app

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"bottrade/internal/api"
)

// These tests exercise the Mongo scheduled-close layer without a live database:
// the claim precondition (the whole single-winner story lives in this filter) and
// the BSON round-trip of the persisted row. Actual FindOneAndUpdate atomicity still
// needs an integration harness — tracked in docs/architecture/durable-close-followups.md.

func bsonLookup(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// The claim filter is the entire single-winner guard: a row is claimable only when
// it is pending-and-due, or executing-and-due-and-stale (reclaim after a crash). A
// typo in an operator or field name here would silently allow a double close.
func TestScheduledCloseClaimFilterBranches(t *testing.T) {
	now := time.Unix(1710000000, 0).UTC()

	// Without an id (poller ListDue): no _id constraint, just the $or.
	filter := scheduledCloseClaimFilter("", now)
	if _, ok := bsonLookup(filter, "_id"); ok {
		t.Fatalf("id-less filter must not constrain _id: %+v", filter)
	}
	orVal, ok := bsonLookup(filter, "$or")
	if !ok {
		t.Fatalf("filter missing $or: %+v", filter)
	}
	branches, ok := orVal.(bson.A)
	if !ok || len(branches) != 2 {
		t.Fatalf("$or = %+v, want exactly two branches (pending-due, stale-executing)", orVal)
	}

	pending, ok := branches[0].(bson.D)
	if !ok {
		t.Fatalf("branch[0] not a bson.D: %+v", branches[0])
	}
	if st, _ := bsonLookup(pending, "status"); st != api.ScheduledCloseStatusPending {
		t.Fatalf("branch[0] status = %v, want pending", st)
	}
	if _, ok := bsonLookup(pending, "due_at"); !ok {
		t.Fatalf("branch[0] must gate on due_at: %+v", pending)
	}

	stale, ok := branches[1].(bson.D)
	if !ok {
		t.Fatalf("branch[1] not a bson.D: %+v", branches[1])
	}
	if st, _ := bsonLookup(stale, "status"); st != api.ScheduledCloseStatusExecuting {
		t.Fatalf("branch[1] status = %v, want executing", st)
	}
	// The stale-reclaim branch must gate on BOTH due_at and updated_at, else a
	// freshly-claimed row would be reclaimed by another instance immediately.
	if _, ok := bsonLookup(stale, "due_at"); !ok {
		t.Fatalf("branch[1] must gate on due_at: %+v", stale)
	}
	if _, ok := bsonLookup(stale, "updated_at"); !ok {
		t.Fatalf("branch[1] must gate on updated_at (stale window): %+v", stale)
	}

	// With an id (ClaimDue on a single row): the _id constraint is added.
	byID := scheduledCloseClaimFilter("close_1", now)
	if v, ok := bsonLookup(byID, "_id"); !ok || v != "close_1" {
		t.Fatalf("id filter _id = %v (ok=%v), want close_1", v, ok)
	}
}

// The row that survives a restart must round-trip through BSON with every field the
// poller and reconciler depend on — especially Side (H1 guard), Status, and the
// optional pointer/omitempty fields.
func TestScheduledCloseBSONRoundTrip(t *testing.T) {
	now := time.Unix(1710000000, 0).UTC()
	purge := now.Add(90 * 24 * time.Hour)
	row := api.ScheduledClose{
		ID:                  "close_rt",
		UserKey:             "tg:7",
		UserID:              7,
		Symbol:              "BTCUSDT",
		Side:                "long",
		DueAt:               now.Add(15 * time.Minute),
		WindowSeconds:       900,
		EntryConfirmationID: "conf-123",
		Status:              api.ScheduledCloseStatusPending,
		ConfirmationID:      "conf-close",
		Reason:              "mission timed close",
		CreatedAt:           now,
		UpdatedAt:           now,
		PurgeAt:             &purge,
	}

	data, err := bson.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got api.ScheduledClose
	if err := bson.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != row.ID || got.UserKey != row.UserKey || got.UserID != row.UserID ||
		got.Symbol != row.Symbol || got.Side != row.Side || got.WindowSeconds != row.WindowSeconds ||
		got.EntryConfirmationID != row.EntryConfirmationID || got.Status != row.Status ||
		got.ConfirmationID != row.ConfirmationID || got.Reason != row.Reason {
		t.Fatalf("round-trip = %+v, want fields preserved", got)
	}
	if !got.DueAt.Equal(row.DueAt) || !got.CreatedAt.Equal(row.CreatedAt) || !got.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("round-trip times = %+v, want preserved", got)
	}
	if got.PurgeAt == nil || !got.PurgeAt.Equal(purge) {
		t.Fatalf("round-trip purge_at = %v, want %v", got.PurgeAt, purge)
	}

	// A legacy/awaiting row leaves Side, ConfirmationID and PurgeAt empty; omitempty
	// must keep those keys out of the document so TTL and side-match behave.
	awaiting := api.ScheduledClose{
		ID: "close_await", UserID: 7, Symbol: "BTCUSDT",
		Status: api.ScheduledCloseStatusAwaitingEntry, EntryConfirmationID: "conf-9",
	}
	adata, err := bson.Marshal(awaiting)
	if err != nil {
		t.Fatalf("marshal awaiting: %v", err)
	}
	var raw bson.M
	if err := bson.Unmarshal(adata, &raw); err != nil {
		t.Fatalf("unmarshal awaiting: %v", err)
	}
	for _, k := range []string{"side", "confirmation_id", "purge_at"} {
		if _, present := raw[k]; present {
			t.Fatalf("awaiting doc should omit %q, got %+v", k, raw)
		}
	}
}
