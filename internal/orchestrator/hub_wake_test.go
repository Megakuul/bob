package orchestrator

import (
	"testing"
	"time"
)

// TestWakeBurstSpreadsOverWorkers: two enqueues within one SlotState period wake two different
// idle workers (the woken one counts one slot less until its next SlotState). Before, both
// wake-ups went to whichever worker won the random tie both times, half the time the same one.
func TestWakeBurstSpreadsOverWorkers(t *testing.T) {
	for seed := range uint64(20) {
		h := NewHub(nil)
		a, b := h.Attach("wa"), h.Attach("wb")
		a.SetSlots(4)
		b.SetSlots(4)
		d := &Dispatcher{Workers: h.Workers(), SecondWakeAfter: time.Hour, Seed: seed}
		first := d.candidates("", nil)[0]
		first.Wake("c", 1, "j1")
		second := d.candidates("", nil)[0]
		if second.ID() == first.ID() {
			t.Fatalf("seed %d: both wake-ups of a burst went to %s", seed, first.ID())
		}
		if got := first.FreeSlots(); got != 3 {
			t.Fatalf("seed %d: woken worker has %d free slots, want 3 until its next SlotState", seed, got)
		}
		first.(*Conn).SetSlots(4) // the next SlotState is the truth again
		if got := first.FreeSlots(); got != 4 {
			t.Fatalf("seed %d: SlotState did not reset the free slots: %d", seed, got)
		}
	}
}
