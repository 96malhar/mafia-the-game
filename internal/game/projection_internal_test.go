package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// This is an INTERNAL test file (package game, not game_test) and only
// exists to test invariants that cannot be expressed through the public
// API. Specifically: that canSee defaults to deny when the
// Visibility.Audience tag is unknown.
//
// The Event interface is closed (unexported isEvent marker), so external
// test code cannot construct a fake event with a weird audience tag.
// Keep this file SHORT — anything that can be tested externally should
// live in projection_test.go.

// fakeUnknownAudienceEvent implements Event (via the unexported isEvent
// marker, which is only reachable from inside this package) and reports
// an Audience value that canSee should not recognize.
type fakeUnknownAudienceEvent struct{}

func (fakeUnknownAudienceEvent) isEvent() {}
func (fakeUnknownAudienceEvent) Visibility() Visibility {
	return Visibility{Audience: "definitely-not-a-real-audience"}
}

// State details don't matter for this test — only the audience tag.
func TestProjection_DefaultsDenyUnknownAudience(t *testing.T) {
	state := newState()
	events := []Event{fakeUnknownAudienceEvent{}}

	for _, viewer := range []PlayerID{"alice", "bob", ""} {
		out := Project(viewer, events, state)
		require.Empty(t, out,
			"unknown-audience event must be hidden from viewer %q", viewer)
	}
}
