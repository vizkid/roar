package rex

import (
	"math"
	"strings"
)

// Rect is a pane's place in its window: x, y, width, and height, each a
// fraction of the window.
type Rect [4]float64

type layer struct {
	Kind   string `json:"kind"`
	Layout *node  `json:"layout"`
}

// node is one split or leaf of a tiled layout.
type node struct {
	Rect    []float64 `json:"rect"`
	BlockID string    `json:"block_id"`
	Split   *struct {
		Before *node `json:"before"`
		After  *node `json:"after"`
	} `json:"split"`
}

// Rects maps each pane in the window's tiled layers to its place. Floating
// panes have no edge to name, so they are left out.
func (w Window) Rects() map[string]Rect {
	rects := map[string]Rect{}
	var walk func(n *node)
	walk = func(n *node) {
		switch {
		case n == nil:
		case n.Split != nil:
			walk(n.Split.Before)
			walk(n.Split.After)
		case n.BlockID != "" && len(n.Rect) == 4:
			rects[n.BlockID] = Rect{n.Rect[0], n.Rect[1], n.Rect[2], n.Rect[3]}
		}
	}
	for _, l := range w.Layers {
		if l.Kind == "tiled" {
			walk(l.Layout)
		}
	}
	return rects
}

// edges says, for each position word, which edge a pane must touch.
var edges = map[string]func(r Rect, min, max [2]float64) bool{
	"left":   func(r Rect, min, _ [2]float64) bool { return near(r[0], min[0]) },
	"right":  func(r Rect, _, max [2]float64) bool { return near(r[0]+r[2], max[0]) },
	"top":    func(r Rect, min, _ [2]float64) bool { return near(r[1], min[1]) },
	"bottom": func(r Rect, _, max [2]float64) bool { return near(r[1]+r[3], max[1]) },
}

// atPosition returns the panes on the edge, or both edges for a corner such
// as top-left, that hint names. It returns nil for any other word.
func atPosition(rects map[string]Rect, hint string) []string {
	words := strings.Split(hint, "-")
	switch {
	case len(words) == 1 && edges[hint] != nil:
	case len(words) == 2 && (words[0] == "top" || words[0] == "bottom") && (words[1] == "left" || words[1] == "right"):
	default:
		return nil
	}
	min, max := [2]float64{math.Inf(1), math.Inf(1)}, [2]float64{math.Inf(-1), math.Inf(-1)}
	for _, r := range rects {
		min[0], min[1] = math.Min(min[0], r[0]), math.Min(min[1], r[1])
		max[0], max[1] = math.Max(max[0], r[0]+r[2]), math.Max(max[1], r[1]+r[3])
	}
	var hits []string
	for id, r := range rects {
		ok := true
		for _, w := range words {
			ok = ok && edges[w](r, min, max)
		}
		if ok {
			hits = append(hits, id)
		}
	}
	return hits
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }
