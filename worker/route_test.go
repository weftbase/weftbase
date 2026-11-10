package worker

import (
	"testing"
	"time"
)

func TestRouterPicksByVersionAndHolder(t *testing.T) {
	var r Router
	a := &Worker{ID: "wk_a", Versions: []string{"agv_1"}}
	b := &Worker{ID: "wk_b", Versions: []string{"agv_1", "agv_2"}}
	r.Add(a)
	r.Add(b)
	if got := r.Versions(); len(got) != 2 || got[0] != "agv_1" || got[1] != "agv_2" {
		t.Errorf("Versions = %v", got)
	}
	if w, ok := r.Pick("agv_2", ""); !ok || w != b {
		t.Errorf("agv_2 → %v", w)
	}
	if _, ok := r.Pick("agv_3", ""); ok {
		t.Error("a version nobody serves was routed")
	}
	r.Hold(a, "agv_1", 1)
	if w, _ := r.Pick("agv_1", ""); w != b {
		t.Errorf("least loaded = %s, want wk_b", w.ID)
	}
	if w, ok := r.Pick("agv_1", "wk_a"); !ok || w != a {
		t.Errorf("holder = %v", w)
	}
	if _, ok := r.Pick("agv_2", "wk_a"); ok {
		t.Error("the holder does not serve agv_2, yet a worker was picked")
	}
	r.Remove("wk_b")
	if got := r.Versions(); len(got) != 1 {
		t.Errorf("after Remove = %v", got)
	}
	r.Add(&Worker{ID: "wk_a", Versions: []string{"agv_9"}})
	if len(r.Workers()) != 1 || r.Versions()[0] != "agv_9" {
		t.Errorf("re-Add did not replace: %v", r.Versions())
	}
}

// TestRouterCapacity: a worker at its version's capacity, or paused
// after a busy answer, takes no new start of it and its route is not
// Ready; a run leaving its hands frees the slot and ends the pause.
func TestRouterCapacity(t *testing.T) {
	var r Router
	a := &Worker{ID: "wk_a", Versions: []string{"agv_1", "agv_2"}, Capacity: map[string]int{"agv_1": 1}}
	r.Add(a)
	now := time.Now()
	if got := r.Ready(now); len(got) != 2 {
		t.Fatalf("Ready = %v", got)
	}
	r.Hold(a, "agv_1", 1)
	if got := r.Ready(now); len(got) != 1 || got[0] != "agv_2" {
		t.Errorf("Ready at capacity = %v, want [agv_2]", got)
	}
	if _, ok := r.Pick("agv_1", ""); ok {
		t.Error("a full worker was picked for a new start")
	}
	if w, ok := r.Pick("agv_1", "wk_a"); !ok || w != a {
		t.Error("the holder of a run is still its route when full")
	}
	r.Hold(a, "agv_1", -1)
	if got := r.Ready(now); len(got) != 2 {
		t.Errorf("Ready after a run left = %v", got)
	}
	r.Pause(a, "agv_2", now.Add(time.Hour))
	if got := r.Ready(now); len(got) != 1 || got[0] != "agv_1" {
		t.Errorf("Ready while agv_2 is paused = %v", got)
	}
	if got := r.Ready(now.Add(2 * time.Hour)); len(got) != 2 {
		t.Errorf("Ready after the pause = %v", got)
	}
	r.Hold(a, "agv_2", 1)
	r.Hold(a, "agv_2", -1)
	if got := r.Ready(now); len(got) != 2 {
		t.Errorf("a run leaving did not end the pause: %v", got)
	}
}
