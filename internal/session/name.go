package session

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// generateName returns a Docker-style auto-name in the shape
// "<agent>-<adjective>_<scientist>", e.g. "claude-witty_curie".
// Collisions with existing names are the caller's problem to handle
// (retry on a UNIQUE-constraint violation).
func generateName(agent string) string {
	return fmt.Sprintf("%s-%s_%s", agent, pickFrom(adjectives), pickFrom(scientists))
}

// pickFrom returns a cryptographically-random element of the slice.
// Using crypto/rand keeps name generation deterministic-only in the
// "fully predictable" sense; no shared math/rand seed footgun.
func pickFrom(words []string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing on a working system is fatal. Panic
		// here is acceptable; this is name generation, not a hot
		// path that needs graceful degradation.
		panic(fmt.Sprintf("session: rand.Read: %v", err))
	}
	n := binary.BigEndian.Uint64(b[:])
	return words[n%uint64(len(words))]
}

// Wordlists kept small on purpose; collision probability with ~30 × ~30 = 900 combinations is
// tolerable for a personal-scale node and uniqueness is enforced by the DB anyway.

var adjectives = []string{
	"witty", "bold", "calm", "eager", "fierce", "gentle", "happy",
	"jolly", "keen", "lively", "merry", "nimble", "proud", "quick",
	"sharp", "swift", "wise", "zealous", "brave", "clever",
	"curious", "daring", "elegant", "honest", "kind", "lucky",
	"modest", "noble", "patient", "vivid",
}

var scientists = []string{
	"curie", "darwin", "einstein", "feynman", "galileo", "hopper",
	"kepler", "lovelace", "newton", "pasteur", "tesla", "turing",
	"bohr", "fermi", "noether", "ramanujan", "hawking", "shannon",
	"euler", "gauss", "leibniz", "maxwell", "planck", "schroedinger",
	"watson", "berners", "thompson", "kahneman", "knuth", "ritchie",
}
