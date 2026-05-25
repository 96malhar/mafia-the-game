package room

import (
	"sync/atomic"

	"github.com/malhar/mafia-the-game/internal/game"
)

// outboundChanCapacity is the buffer size on each subscriber's outgoing
// channel. Picked to absorb a small burst (e.g. a full game's worth of
// startup events for a new joiner) without making a slow subscriber
// hold up the room.
//
// Hitting capacity is interpreted as "this subscriber is too slow" and
// causes the room to disconnect them. See Room.broadcast.
const outboundChanCapacity = 64

// Subscriber represents one active connection to a room — typically one
// WebSocket, but in tests it's just a channel reader. It is created by
// the transport layer (or a test) and handed to the room via inJoin /
// inRejoin.
//
// A Subscriber has a stable identity per CONNECTION (not per player).
// The PlayerID field is empty until the room accepts the subscriber via
// inJoin/inRejoin; thereafter it is the authoritative player identity
// the room will attribute future commands to.
type Subscriber struct {
	// playerID is set by the room after a successful join/rejoin and
	// is read-only thereafter. We use atomic so external readers
	// (logging, metrics) can read it safely while the room writes it.
	playerID atomic.Value // holds game.PlayerID

	// out is the room->subscriber channel. The room is the only sender;
	// the subscriber's reader (WebSocket write pump in production, test
	// goroutine in tests) is the only receiver.
	out chan outbound
}

// NewSubscriber constructs a Subscriber ready to be passed to a room.
// The PlayerID is empty until the room assigns one.
func NewSubscriber() *Subscriber {
	return &Subscriber{
		out: make(chan outbound, outboundChanCapacity),
	}
}

// PlayerID returns the assigned identity, or "" if not yet joined.
func (s *Subscriber) PlayerID() game.PlayerID {
	v, _ := s.playerID.Load().(game.PlayerID)
	return v
}

// Outbound returns the channel the subscriber should read from.
// The channel is closed when the room finishes broadcasting to this
// subscriber (i.e. on Leave or room shutdown).
func (s *Subscriber) Outbound() <-chan outbound {
	return s.out
}

// setPlayerID is called by the room when accepting a join/rejoin.
func (s *Subscriber) setPlayerID(id game.PlayerID) {
	s.playerID.Store(id)
}
