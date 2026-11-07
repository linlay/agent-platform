package commands

import (
	"fmt"
	"strings"
	"testing"
)

// findWalk reads -maxdepth and a leading prune clause; anything that changes
// what -prune or the depth limit means drops that part of the bound.
func TestFindWalkBounds(t *testing.T) {
	for command, want := range map[string]string{
		"find R -name x":                                        "none",
		"find R -maxdepth 2 -name x":                            "depth=2 prune=[]",
		"find R -maxdepth 2 -maxdepth 3":                        "none",
		"find R -path R/.state -prune -o -print":                "depth=-1 prune=[[-path=R/.state]]",
		"find R -type d -name .state -prune":                    "depth=-1 prune=[[-type=d -name=.state]]",
		"find R ( -path A -o -name B ) -prune -o -name x":       "depth=-1 prune=[[-path=A] [-name=B]]",
		`find R -maxdepth 4 \( -path A -o -path B \) -prune -o`: "depth=4 prune=[[-path=A] [-path=B]]",
		"find R -wholename A -prune -o -print":                  "depth=-1 prune=[[-path=A]]",
		"find R -prune":                                         "depth=-1 prune=[[]]",
		"find R -name x -o -path A -prune":                      "none",
		"find R ! -path A -prune -o -print":                     "none",
		"find R -path A -prune -print":                          "none",
		"find R -iname A -prune -o -print":                      "none",
		"find R -type f -name A -prune -o -print":               "none",
		"find R -depth -path A -prune -o -print":                "none",
		"find R -maxdepth 2 -delete":                            "depth=2 prune=[]",
		"find R -mindepth 1 -path A -prune -o -print":           "none",
		"find -L R -maxdepth 2 -path A -prune -o -print":        "none",
		"find R -follow -path A -prune -o -print":               "none",
	} {
		args := strings.Fields(command)[1:]
		start, follow := 0, false
		for args[start] == "-L" {
			follow, start = true, start+1
		}
		w := findWalk(args, start+1, follow)
		got := "none"
		if w != nil {
			var alts []string
			for _, alt := range w.Prune {
				var tests []string
				for _, test := range alt {
					tests = append(tests, test.Primary+"="+test.Value)
				}
				alts = append(alts, "["+strings.Join(tests, " ")+"]")
			}
			got = fmt.Sprintf("depth=%d prune=[%s]", w.MaxDepth, strings.Join(alts, " "))
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", command, got, want)
		}
	}
}

// Calls that run programs or misparse lose the bound, since a program run on
// an ancestor could still reach a pruned directory.
func TestFindWalkOnlyOnCompleteWalks(t *testing.T) {
	for args, want := range map[string]bool{
		"R -maxdepth 1":                 true,
		"R -maxdepth 1 -exec rm {} ;":   false,
		"R -maxdepth 1 -execdir ls ;":   false,
		"R -maxdepth 1 -made-up-option": false,
	} {
		e := findOperands(strings.Fields(args))
		if got := e.Files[0].Walk != nil; got != want {
			t.Errorf("%s: walk kept %v, want %v", args, got, want)
		}
	}
}
