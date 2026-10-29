package shellanalysis

import "testing"

func TestLsofQuerySelectorsAndUnknownOperations(t *testing.T) {
	for _, args := range [][]string{
		{"-nP", "-iTCP:8080", "-iTCP:8081", "-iTCP:8082", "-sTCP:LISTEN"},
		{"-nP", "-i", ":8080"},
		{"-a", "-p", "123", "-iTCP", "-s", "TCP:LISTEN"},
		{"-i"},
		{"-p123", "-uowner", "-cserver", "-Fpcn"},
	} {
		if e := Operands("lsof", args); e.Incomplete || e.ExecutesCode || len(e.Files) != 0 || e.RemoteMutation {
			t.Fatalf("query %q: %+v", args, e)
		}
	}
	for _, args := range [][]string{
		{"-D"}, {"-Db/tmp/cache"}, {"-Du", "/tmp/cache"},
		{"+D", "/outside"}, {"--unknown"}, {"-nPDu/tmp/cache"}, {"-p"},
	} {
		if e := Operands("lsof", args); !e.Incomplete || !e.ExecutesCode {
			t.Fatalf("unmodeled operation %q must retain opaque review: %+v", args, e)
		}
	}
	e := Operands("lsof", []string{"-nP", "--", "plain.txt", "/outside/file"})
	if e.ExecutesCode || len(e.Files) != 2 || e.Files[0] != (FileAccess{Path: "plain.txt"}) || e.Files[1] != (FileAccess{Path: "/outside/file"}) {
		t.Fatalf("file operands must retain read checks: %+v", e)
	}
}
