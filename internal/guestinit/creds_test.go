//go:build linux

package guestinit

import "testing"

func TestFingerprintDetectsChange(t *testing.T) {
	base := map[string][]byte{".claude/.credentials.json": []byte(`{"token":"a"}`)}
	same := map[string][]byte{".claude/.credentials.json": []byte(`{"token":"a"}`)}
	changed := map[string][]byte{".claude/.credentials.json": []byte(`{"token":"b"}`)}

	if fingerprint(base) != fingerprint(same) {
		t.Error("identical bundles must fingerprint the same, or every poll would push again")
	}
	if fingerprint(base) == fingerprint(changed) {
		t.Error("a refreshed token must change the fingerprint, or the login would never reach the host")
	}
}

// Map iteration order is random, so an order dependent fingerprint would push on every poll.
func TestFingerprintIsOrderIndependent(t *testing.T) {
	a := map[string][]byte{"one": []byte("1"), "two": []byte("2"), "three": []byte("3")}
	first := fingerprint(a)
	for range 20 {
		if got := fingerprint(a); got != first {
			t.Fatalf("fingerprint is not stable across iterations: %s vs %s", got, first)
		}
	}
}

// Adding a file is a real change: a login can write a second file.
func TestFingerprintCoversFileSet(t *testing.T) {
	one := map[string][]byte{"a": []byte("x")}
	two := map[string][]byte{"a": []byte("x"), "b": []byte("y")}
	if fingerprint(one) == fingerprint(two) {
		t.Error("adding a credential file must change the fingerprint")
	}
}
