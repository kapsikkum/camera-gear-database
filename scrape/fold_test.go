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

func TestMakerName(t *testing.T) {
	for label, want := range map[string]string{
		"Canon Inc.": "Canon", "Nikon Corporation": "Nikon", "Asahi Optical Co., Ltd.": "Pentax",
		"Victor Hasselblad AB": "Hasselblad", "Minolta": "Minolta", "Ernst Leitz GmbH": "Ernst Leitz",
	} {
		if got := makerName(label); got != want {
			t.Errorf("%q: got %q, want %q", label, got, want)
		}
	}
}

func TestFilmFormat(t *testing.T) {
	for _, c := range []struct {
		it   item
		want string
	}{
		{item{Name: "Canon AE-1", Brand: "Canon"}, "35mm"},
		{item{Name: "Pentax 6x7", Brand: "Pentax"}, "120"},
		{item{Name: "Pentax 645N", Brand: "Pentax"}, "120"},
		{item{Name: "Hasselblad 500C", Brand: "Hasselblad"}, "120"},
		{item{Name: "Mamiya Sekor 80mm f/2.8", Brand: "Mamiya", Kind: "lens"}, "120"},
		{item{Name: "Canon EOS IX", Brand: "Canon"}, "APS"},
		{item{Name: "Sinar p2", Brand: "Sinar"}, "sheet"},
		{item{Name: "Nikon F601", Brand: "Nikon"}, "35mm"},
	} {
		if got := filmFormat(&c.it); got != c.want {
			t.Errorf("%s: got %q, want %q", c.it.Name, got, c.want)
		}
	}
}

func TestFilmlessDropsJunk(t *testing.T) {
	for _, c := range []struct {
		it   item
		want bool
	}{
		{item{Name: "Samsung Galaxy S21 Rear Main Camera", Kind: "lens", Brand: "Samsung"}, true},
		{item{Name: "Olympus OM system", Kind: "body", Brand: "Olympus"}, true},
		{item{Name: "Cooke Varotal 20-100mm T3.1", Kind: "lens", Brand: "Cooke"}, true},
		{item{Name: "Minolta AF DT 18-70mm f/3.5-5.6 lens", Kind: "lens", Brand: "Minolta"}, true},
		{item{Name: "Canon T60", Kind: "body", Brand: "Cosina"}, false}, // T60 is a film SLR, not a T-stop
		{item{Name: "Olympus OM-1", Kind: "body", Brand: "Olympus"}, false},
	} {
		if got := filmless(&c.it); got != c.want {
			t.Errorf("%s: got %v, want %v", c.it.Name, got, c.want)
		}
	}
	if got := filmFormat(&item{Name: "Polaroid SX-70", Brand: "Polaroid"}); got != "instant" {
		t.Errorf("Polaroid SX-70: %q", got)
	}
}

func TestGuessMount(t *testing.T) {
	for _, c := range []struct{ name, brand, want string }{
		{"Nikon F3", "Nikon", "Nikon F"},
		{"Nikon AF DC-Nikkor 105mm f/2D", "Nikon", "Nikon F"},
		{"Pentax K1000", "Pentax", "Pentax K"},
		{"Pentax 67II", "Pentax", "Pentax 67"},
		{"Asahi Pentax Spotmatic", "Pentax", "M42"},
		{"Zenit E", "Zenit", "M42"},
		{"Minolta SR-T 101", "Minolta", "Minolta SR"},
		{"Minolta Maxxum 7000 AF", "Minolta", "Minolta A"},
		{"Olympus OM-1", "Olympus", "Olympus OM"},
		{"Hasselblad 500C/M", "Hasselblad", "Hasselblad V"},
		{"Sigma 24mm F1.4 DG HSM Art", "Sigma", ""}, // third party, mount unknown
	} {
		if got := guessMount(&item{Name: c.name, Brand: c.brand}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
