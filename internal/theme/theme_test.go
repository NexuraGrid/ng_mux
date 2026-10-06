package theme

import (
	"reflect"
	"testing"
)

// colours lists every colour a palette sets, by field name.
func colours(p Palette) map[string]int {
	out := map[string]int{"StatusFG": p.StatusFG, "StatusBG": p.StatusBG}
	v := reflect.ValueOf(p)
	for i := 0; i < v.NumField(); i++ {
		if st, ok := v.Field(i).Interface().(Style); ok {
			name := v.Type().Field(i).Name
			out[name+".FG"], out[name+".BG"] = st.FG, st.BG
		}
	}
	return out
}

// Every theme must render on old terminals: the 8 base colours plus
// bright-black at most, never the 256-colour or truecolour range.
func TestThemesUseOnlyBaseColours(t *testing.T) {
	for _, name := range Names() {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed for a listed name", name)
		}
		for field, c := range colours(p) {
			if c != Default && c != Inherit && (c < 0 || c > 8) {
				t.Errorf("%s.%s = %d, want 0..8, Default or Inherit", name, field, c)
			}
		}
	}
}

func TestMonoUsesNoColour(t *testing.T) {
	for field, c := range colours(Mono()) {
		if c != Default && c != Inherit {
			t.Errorf("mono %s = %d, want Default or Inherit", field, c)
		}
	}
	m := Mono()
	if m.BorderActive == m.BorderDim {
		t.Error("mono must still tell the focused pane's borders apart")
	}
}

// Light must not put yellow text on the light bar or as an accent.
func TestLightAvoidsYellowText(t *testing.T) {
	for field, c := range colours(Light()) {
		if c == 3 && field[len(field)-2:] == "FG" {
			t.Errorf("light %s is yellow text", field)
		}
	}
}

func TestLookupAndNames(t *testing.T) {
	if got := Names(); !reflect.DeepEqual(got, []string{"dark", "light", "mono"}) {
		t.Errorf("Names() = %v", got)
	}
	if _, ok := Lookup("solarized"); ok {
		t.Error("Lookup of an unknown theme should fail")
	}
	if p, _ := Lookup(DefaultName); p != Dark() {
		t.Error("the default theme should be dark")
	}
}
