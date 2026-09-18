package main

import "testing"

func TestFold(t *testing.T) {
	items := []*item{
		{Name: "Canon EF 28–90mm lens", Kind: "lens", Wikipedia: "https://en.wikipedia.org/wiki/Canon_EF_28-90mm_lens"},
		{Name: "Canon EF 28-90mm F4-5.6", Kind: "lens"},
		{Name: "Canon EF 28–105mm lens", Kind: "lens"}, // no specific model: worth keeping
		{Name: "Canon AE-1", Kind: "body"},
	}
	got := fold(items)
	var names []string
	for _, it := range got {
		names = append(names, it.Name)
	}
	want := []string{"Canon EF 28-90mm F4-5.6", "Canon EF 28-105mm lens", "Canon AE-1"}
	if len(names) != len(want) {
		t.Fatalf("got %q, want %q", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("item %d: got %q, want %q", i, names[i], want[i])
		}
	}
	if got[0].Wikipedia == "" {
		t.Error("the folded-away entry's article was lost")
	}
}
