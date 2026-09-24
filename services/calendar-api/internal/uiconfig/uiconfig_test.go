package uiconfig

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"bscalendar/fixtures"
)

func svc(t *testing.T) *Service {
	t.Helper()
	sch, err := compileSchema()
	if err != nil {
		t.Fatal(err)
	}
	return &Service{schema: sch}
}

func mutate(t *testing.T, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fixtures.UIConfigDefault, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ := json.Marshal(m)
	return b
}

func TestDefaultConfigIsValid(t *testing.T) {
	if rep := svc(t).Validate(fixtures.UIConfigDefault); !rep.Valid {
		t.Fatalf("the built-in default must pass its own schema and contrast rules: %+v", rep.Errors)
	}
}

func TestSchemaAndContrastRules(t *testing.T) {
	s := svc(t)
	cases := map[string]struct {
		cfg  []byte
		path string
	}{
		"unknown top-level field": {mutate(t, func(m map[string]any) { m["extra"] = 1 }), ""},
		"bad colour":              {mutate(t, func(m map[string]any) { m["theme"].(map[string]any)["dark"].(map[string]any)["bg"] = "black" }), "/theme/dark/bg"},
		"missing token":           {mutate(t, func(m map[string]any) { delete(m["theme"].(map[string]any)["light"].(map[string]any), "holiday") }), "/theme/light"},
		"bad enum":                {mutate(t, func(m map[string]any) { m["defaults"].(map[string]any)["mode"] = "XY" }), "/defaults/mode"},
		"radius too big":          {mutate(t, func(m map[string]any) { m["shape"].(map[string]any)["radius"] = 99 }), "/shape/radius"},
		"low text contrast":       {mutate(t, func(m map[string]any) { m["theme"].(map[string]any)["light"].(map[string]any)["text"] = "#EEEEEE" }), "/theme/light/text"},
		"low selected contrast":   {mutate(t, func(m map[string]any) { m["theme"].(map[string]any)["dark"].(map[string]any)["onPrimary"] = "#5A9EF0" }), "/theme/dark/onPrimary"},
		"not json":                {[]byte(`{"theme":`), ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rep := s.Validate(c.cfg)
			if rep.Valid || len(rep.Errors) == 0 {
				t.Fatal("expected invalid")
			}
			if c.path == "" {
				return
			}
			for _, e := range rep.Errors {
				if strings.HasPrefix(e.Path, c.path) {
					return
				}
			}
			t.Fatalf("no error at %s: %+v", c.path, rep.Errors)
		})
	}
}

func TestLuminanceAndContrastRatio(t *testing.T) {
	w, _ := luminance("#FFFFFF")
	b, _ := luminance("#000000")
	if ratio := (w + 0.05) / (b + 0.05); math.Abs(ratio-21) > 0.01 {
		t.Fatalf("white on black must be 21:1, got %.2f", ratio)
	}
	if _, ok := luminance("red"); ok {
		t.Fatal("named colours are not accepted")
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"1.2.3", "1.2.4", true}, {"1.10.0", "1.9.0", false}, {"2.0.0", "10.0.0", true},
		{"1.0.0", "1.0.0", false}, {"bad", "0.0.1", true}, {"0.0.1", "bad", false},
	}
	for _, c := range cases {
		if got := SemverLess(c.a, c.b); got != c.less {
			t.Errorf("SemverLess(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestValidApp(t *testing.T) {
	for app, want := range map[string]bool{"web": true, "mobile-ios": true, "a": false, "Web": false, "1app": false, "x_y": false} {
		if ValidApp(app) != want {
			t.Errorf("ValidApp(%q) != %v", app, want)
		}
	}
}
