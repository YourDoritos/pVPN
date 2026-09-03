package daemon

import "testing"

// The kernel emits several RTM_NEWLINK messages per real transition, and
// container/VM runtimes churn veths continuously on an idle machine.
// Only a genuine up/carrier change should reach the debouncer.
func TestNetworkMonitor_LinkDedup(t *testing.T) {
	m := &networkMonitor{seenLinks: make(map[int]linkState)}

	up := linkState{up: true, carrier: true}
	down := linkState{up: false, carrier: false}

	if !m.transitionedLink(2, up) {
		t.Error("first sighting of a link should count as a transition")
	}
	for i := 0; i < 5; i++ {
		if m.transitionedLink(2, up) {
			t.Fatalf("repeat %d of an unchanged state counted as a transition", i)
		}
	}
	if !m.transitionedLink(2, down) {
		t.Error("up -> down should count as a transition")
	}
	if m.transitionedLink(2, down) {
		t.Error("repeated down should not count as a transition")
	}

	// Carrier loss on an admin-up link is a real change.
	m.transitionedLink(3, up)
	if !m.transitionedLink(3, linkState{up: true, carrier: false}) {
		t.Error("carrier loss should count as a transition")
	}
}

// Links are tracked per ifindex; churning veths must not bleed into each
// other, and removal must free the entry so the map cannot grow forever.
func TestNetworkMonitor_LinkForget(t *testing.T) {
	m := &networkMonitor{seenLinks: make(map[int]linkState)}
	up := linkState{up: true, carrier: true}

	m.transitionedLink(10, up)
	if !m.transitionedLink(11, up) {
		t.Error("a different ifindex should be tracked separately")
	}

	m.forgetLink(10)
	if len(m.seenLinks) != 1 {
		t.Errorf("after forgetting one of two links, map has %d entries, want 1", len(m.seenLinks))
	}
	if !m.transitionedLink(10, up) {
		t.Error("a re-created ifindex should count as a new transition")
	}
}
