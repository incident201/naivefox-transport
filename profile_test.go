package transport

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Freeze the previously measured parameters while replacing positional profile
// literals with named inheritance. New profiles are deliberately outside this set.
func TestPreviouslyMeasuredProfilesFrozen(t *testing.T) {
	names := strings.Fields("v1 duplex-v1 compact compact-sync compact-sync20 compact-fast20 staged staged-fast staged-fast20 staged-stream20 staged-commit20 continuous-v1 continuous-sync continuous-sync2 continuous-bulk continuous-bulk-ready continuous-bulk-frames continuous-bulk-duplex continuous-bulk-interactive1 continuous-bulk-upload1")
	frozen := make(map[string]appProfile)
	for _, name := range names {
		frozen[name] = profiles[name]
	}
	body, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(body))
	if got != "52e73811661919f765e75c444927a42abf28e383a50b58068f2b1dd9350372da" {
		t.Fatal(got)
	}
}

func TestWindowProfileChangesOnlyCredit(t *testing.T) {
	got := profiles["continuous-bulk-window512"]
	want := profiles["continuous-bulk-duplex"]
	if got.ReceiveWindow != 524288 || want.ReceiveWindow != 0 {
		t.Fatal("window")
	}
	got.ReceiveWindow = 0
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatal("confounded profile")
	}
}
