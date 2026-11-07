package commands

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/shellreview/types"
)

// Go analyzers for names outside the registry: each case lists the visible
// targets as kind:path, or "exec" when the call stays opaque.
func TestProgramAnalyzers(t *testing.T) {
	for command, want := range map[string][]string{
		"sips -g pixelWidth -g pixelHeight a.png":                            {"read:a.png"},
		"sips -s format png -z 83 190 a.webp --out thumbs":                   {"output:thumbs", "read:a.webp"},
		"sips -Z 512 a.png":                                                  {"output:a.png"},
		"sips -m profile.icc a.png -o b.png":                                 {"output:b.png", "read:a.png", "read:profile.icc"},
		"sips -x out.icc a.png":                                              {"output:out.icc", "read:a.png"},
		"sips -j script.js a.png":                                            {"exec"},
		"sips -z 83":                                                         {"exec"},
		"diskutil list":                                                      nil,
		"diskutil info -plist disk0":                                         nil,
		"diskutil info /dev/disk3s5":                                         {"read:/dev/disk3s5"},
		"diskutil apfs listSnapshots disk3s5":                                nil,
		"diskutil list -plist external physical":                             nil,
		"diskutil eraseDisk JHFS+ x disk2":                                   {"exec"},
		"diskutil apfs deleteSnapshot disk3s5 -name x":                       {"exec"},
		"diskutil apfs":                                                      {"exec"},
		"diskutil unmount /Volumes/x":                                        {"exec"},
		"sysctl kern.memorystatus_vm_pressure_level kern.memorystatus_level": nil,
		"sysctl -n hw.memsize":                                               nil,
		"sysctl -a":                                                          nil,
		"sysctl --pattern ^vm":                                               nil,
		"sysctl kern.maxfiles=1":                                             {"exec"},
		"sysctl -w kern.maxfiles=1":                                          {"exec"},
		"sysctl -f settings.conf":                                            {"exec"},
		"sysctl -p":                                                          {"exec"},
		"sysctl --system":                                                    {"exec"},
		"tmutil listlocalsnapshots /":                                        {"read:/"},
		"tmutil listlocalsnapshotdates":                                      nil,
		"tmutil listlocalsnapshotdates /Volumes/x":                           {"read:/Volumes/x"},
		"tmutil listlocalsnapshots":                                          {"exec"},
		"tmutil listlocalsnapshots -X /":                                     {"exec"},
		"tmutil deletelocalsnapshots /":                                      {"exec"},
		"tmutil thinlocalsnapshots / 1000000000":                             {"exec"},
		"tmutil startbackup":                                                 {"exec"},
		"tmutil":                                                             {"exec"},
	} {
		fields := strings.Fields(command)
		e := nativeFacts(fields[0], fields[1:]...)
		var got []string
		if e.Has(types.Exec) {
			got = []string{"exec"}
		} else {
			if len(e.Unknowns) > 0 {
				t.Errorf("%s: unexpected unknowns %+v", command, e.Unknowns)
			}
			for _, op := range e.Ops {
				for _, target := range op.Targets {
					got = append(got, string(op.Kind)+":"+target.Path)
				}
			}
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", command, got, want)
		}
	}
}

func TestProgramsAreCapturedOutsideTheRegistry(t *testing.T) {
	names := strings.Join(Programs(), ",")
	for _, name := range []string{"sips", "diskutil", "sysctl", "tmutil", "mdls", "qlmanage", "memory_pressure", "vm_stat"} {
		if !IsProgram(name) || !strings.Contains(","+names+",", ","+name+",") {
			t.Errorf("%s is not captured as a program: %s", name, names)
		}
	}
	if IsProgram("cat") || IsProgram("top") {
		t.Error("registry names are not extra programs")
	}
}
