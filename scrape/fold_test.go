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

func TestFilmless(t *testing.T) {
	for _, c := range []struct {
		it   item
		want bool
	}{
		{item{Name: "Canon EOS 5D Mark IV", Kind: "body", Digital: true}, true},
		{item{Name: "Canon EOS 300", Kind: "body"}, false},
		{item{Name: "Canon EF-M", Kind: "body"}, false}, // a 1991 film SLR, not the mount
		{item{Name: "EF-S18-55mm f/3.5-5.6 USM", Kind: "lens"}, true},
		{item{Name: "Canon RF-S 18-45mm F4.5-6.3 IS STM", Kind: "lens"}, true},
		{item{Name: "Canon zoom lens", Kind: "lens", Mounts: []string{"Canon EF-S lens mount"}}, true},
		{item{Name: "Canon EF 50mm F1.8", Kind: "lens", Mounts: []string{"Canon EF lens mount"}}, false},
	} {
		if got := filmless(&c.it); got != c.want {
			t.Errorf("%s: got %v, want %v", c.it.Name, got, c.want)
		}
	}
}
