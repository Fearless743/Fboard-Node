package kernel

import (
	"testing"

	"github.com/fearless743/fboard-node/internal/model"
)

func TestUserDiff_UUIDChangeRemovesOld(t *testing.T) {
	old := []model.UserSpec{{ID: 1, UUID: "aaa"}}
	newU := []model.UserSpec{{ID: 1, UUID: "bbb"}}
	toAdd, toRemove := UserDiff(old, newU)
	if len(toAdd) != 1 || toAdd[0].UUID != "bbb" {
		t.Fatalf("toAdd = %+v, want [{ID:1 UUID:bbb}]", toAdd)
	}
	if len(toRemove) != 1 || toRemove[0].UUID != "aaa" {
		t.Fatalf("toRemove = %+v, want [{ID:1 UUID:aaa}] (UUID change must remove old credential)", toRemove)
	}
}

func TestUserDiff_IDGoneRemoves(t *testing.T) {
	old := []model.UserSpec{{ID: 1, UUID: "aaa"}, {ID: 2, UUID: "ccc"}}
	newU := []model.UserSpec{{ID: 1, UUID: "aaa"}}
	_, toRemove := UserDiff(old, newU)
	if len(toRemove) != 1 || toRemove[0].ID != 2 {
		t.Fatalf("toRemove = %+v, want [{ID:2}]", toRemove)
	}
}

func TestUserDiff_UnchangedEmpty(t *testing.T) {
	old := []model.UserSpec{{ID: 1, UUID: "aaa"}}
	newU := []model.UserSpec{{ID: 1, UUID: "aaa"}}
	toAdd, toRemove := UserDiff(old, newU)
	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Fatalf("toAdd=%v toRemove=%v, want empty", toAdd, toRemove)
	}
}

func TestUserDiff_EmptyNewRemovesAll(t *testing.T) {
	old := []model.UserSpec{{ID: 1, UUID: "aaa"}, {ID: 2, UUID: "bbb"}}
	toAdd, toRemove := UserDiff(old, nil)
	if len(toAdd) != 0 {
		t.Fatalf("toAdd = %v, want empty", toAdd)
	}
	if len(toRemove) != 2 {
		t.Fatalf("toRemove = %+v, want 2 users", toRemove)
	}
}
