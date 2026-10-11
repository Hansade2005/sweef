package csscolor

import (
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	cases := map[string][4]float64{
		"#f00":                          {255, 0, 0, 1},
		"#00ff0080":                     {0, 255, 0, 128.0 / 255},
		"rgb(10, 20, 30)":               {10, 20, 30, 1},
		"rgba(10,20,30,.5)":             {10, 20, 30, .5},
		"rgb(10 20 30 / 50%)":           {10, 20, 30, .5},
		"hsl(120, 100%, 50%)":           {0, 255, 0, 1},
		"hsl(0deg 100% 25%)":            {128, 0, 0, 1},
		"rebeccapurple":                 {102, 51, 153, 1},
		"transparent":                   {0, 0, 0, 0},
		"oklch(62.8% 0.2577 29.23)":     {255, 0, 0, 1},
		"color-mix(in srgb, red, blue)": {128, 0, 128, 1},
	}
	for in, want := range cases {
		r, ok := Parse(in)
		if !ok {
			t.Errorf("%s: not parsed", in)
			continue
		}
		got := [4]float64{r.Color.R * 255, r.Color.G * 255, r.Color.B * 255, r.Color.A}
		for i := range got {
			if math.Abs(got[i]-want[i]) > 1.5 && i < 3 || i == 3 && math.Abs(got[i]-want[i]) > 0.01 {
				t.Errorf("%s = %v want %v", in, got, want)
				break
			}
		}
	}
	for _, bad := range []string{"", "#ggg", "rgb(1,2)", "notacolor"} {
		if _, ok := Parse(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if r, _ := Parse("currentColor"); !r.CurrentColor {
		t.Error("currentcolor")
	}
}
