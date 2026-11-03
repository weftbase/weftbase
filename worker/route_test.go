package worker

import "testing"

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
	r.Hold(a, 1)
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
