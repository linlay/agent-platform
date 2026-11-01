package options

import (
	"reflect"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func TestOptionValuesKeepTheirResourceRoles(t *testing.T) {
	spec := types.OptionSpec{
		ShortFlags: "q",
		ShortArgs:  map[byte]types.OptionRole{'o': types.OptionWrite, 'f': types.OptionRead},
		Long: map[string]types.OptionRole{
			"output": types.OptionWrite, "file": types.OptionRead,
			"color": types.OptionOptionalValue, "rawfile": types.OptionNameFile,
		},
		NumericShort: true,
	}
	for _, tc := range []struct {
		name       string
		args       []string
		files      []types.FileAccess
		positional []string
		incomplete bool
	}{
		{"combined_short", []string{"-qoout.txt", "in.txt"}, []types.FileAccess{{Path: "out.txt", Write: true}}, []string{"in.txt"}, false},
		{"separate_long", []string{"--output", "out.txt", "in.txt"}, []types.FileAccess{{Path: "out.txt", Write: true}}, []string{"in.txt"}, false},
		{"attached_long", []string{"--output=out.txt"}, []types.FileAccess{{Path: "out.txt", Write: true}}, nil, false},
		{"optional_does_not_consume", []string{"--color", "in.txt"}, nil, []string{"in.txt"}, false},
		{"name_and_file", []string{"--rawfile", "key", "data.txt"}, []types.FileAccess{{Path: "data.txt"}}, nil, false},
		{"after_double_dash", []string{"--", "--output", "-f"}, nil, []string{"--output", "-f"}, false},
		{"numeric_short", []string{"-20", "in.txt"}, nil, []string{"in.txt"}, false},
		{"missing_value_keeps_prior_file", []string{"-f", "input.txt", "--output"}, []types.FileAccess{{Path: "input.txt"}}, nil, true},
		{"unknown_keeps_known_file", []string{"-finput.txt", "--unknown=/outside/out"}, []types.FileAccess{{Path: "input.txt"}, {Path: "/outside/out", Write: true}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(spec, tc.args)
			for i := range got.Files {
				got.Files[i].Ref = types.ValueRef{}
				got.Files[i].Kind = ""
			}
			if got.Incomplete != tc.incomplete || !reflect.DeepEqual(got.Files, tc.files) || !reflect.DeepEqual(got.Positional, tc.positional) {
				t.Fatalf("got %+v; want files=%+v positional=%v incomplete=%v", got, tc.files, tc.positional, tc.incomplete)
			}
		})
	}
}
