package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// generateName returns an auto-name in the shape "<agent>-<adjective>_<scientist>", e.g.
// "claude-witty_curie".
func generateName(agent string) string {
	return fmt.Sprintf("%s-%s_%s", agent, pickFrom(adjectives), pickFrom(scientists))
}

// nameAttempts bounds the search for a free name. Each try is independent, so a clash is likely but
// staying stuck is not.
const nameAttempts = 20

// generateUniqueName returns a generated name no session is using. Falls back to a plain generated
// name, which the unique constraint still catches, when the names cannot be read or every try was
// taken.
func (s *Service) generateUniqueName(ctx context.Context, agent string) string {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return generateName(agent)
	}
	taken := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		taken[sess.Name] = true
	}
	for range nameAttempts {
		if name := generateName(agent); !taken[name] {
			return name
		}
	}
	return generateName(agent)
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

// Wordlists kept small on purpose. 900 combinations collide often enough at ten sessions that
// generateUniqueName checks what is taken; the DB's unique constraint is the backstop.

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
