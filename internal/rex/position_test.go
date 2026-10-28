package rex

import (
	"slices"
	"testing"
)

func TestPositions(t *testing.T) {
	halves := map[string]Rect{"a": {0, 0, 0.5, 1}, "b": {0.5, 0, 0.5, 1}}
	grid := map[string]Rect{"tl": {0, 0, 0.5, 0.5}, "tr": {0.5, 0, 0.5, 0.5}, "bl": {0, 0.5, 0.5, 0.5}, "br": {0.5, 0.5, 0.5, 0.5}}
	// Rex splits leave rounding in the fractions.
	thirds := map[string]Rect{"x": {0, 0, 0.333333, 1}, "y": {0.333333, 0, 0.333334, 1}, "z": {0.666667, 0, 0.333333, 1}}
	cases := []struct {
		rects map[string]Rect
		hint  string
		want  []string
	}{
		{halves, "left", []string{"a"}},
		{halves, "right", []string{"b"}},
		{halves, "top", []string{"a", "b"}},
		{grid, "left", []string{"bl", "tl"}},
		{grid, "bottom-right", []string{"br"}},
		{grid, "top-left", []string{"tl"}},
		{thirds, "right", []string{"z"}},
		{map[string]Rect{"only": {0, 0, 1, 1}}, "bottom-left", []string{"only"}},
		{halves, "middle", nil},
	}
	for _, c := range cases {
		got := atPosition(c.rects, c.hint)
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s in %v: got %v, want %v", c.hint, c.rects, got, c.want)
		}
	}
}
