package mongo

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// A legacy "unnamed" profile may be stored with the profile field empty, null,
// or absent. profileFilter must match all three so it can still be deleted or
// activated, while a named profile matches exactly.
func TestProfileFilter(t *testing.T) {
	named := profileFilter("u1", "testnet")
	if named["profile"] != "testnet" || named["user_id"] != "u1" {
		t.Fatalf("named filter = %v, want exact profile match", named)
	}
	if _, ok := named["$or"]; ok {
		t.Fatalf("named filter should not use $or: %v", named)
	}

	unnamed := profileFilter("u1", "")
	or, ok := unnamed["$or"].([]bson.M)
	if !ok || len(or) != 3 {
		t.Fatalf("unnamed filter $or = %v, want 3 clauses (empty, null, absent)", unnamed["$or"])
	}
	var hasEmpty, hasNull, hasAbsent bool
	for _, clause := range or {
		switch v := clause["profile"].(type) {
		case string:
			if v == "" {
				hasEmpty = true
			}
		case nil:
			hasNull = true
		case bson.M:
			if v["$exists"] == false {
				hasAbsent = true
			}
		}
	}
	if !hasEmpty || !hasNull || !hasAbsent {
		t.Fatalf("unnamed filter missing a case: empty=%v null=%v absent=%v", hasEmpty, hasNull, hasAbsent)
	}
}
