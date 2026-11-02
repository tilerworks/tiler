package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionQueriesDoNotInitializeConfiguration(t *testing.T) {
	for _, state := range []string{"missing", "invalid", "legacy", "reserved"} {
		t.Run(state, func(t *testing.T) {
			home, work := withWorkspace(t)
			path := filepath.Join(home, ".tiler.yaml")
			var content string
			switch state {
			case "invalid":
				content = "invalid: [\n"
				writeProjectConfig(t, home, content)
				writeProjectConfig(t, work, content)
			case "legacy":
				content = "runtime_dir: ~/.tiler/runtime\n"
				writeProjectConfig(t, home, content)
			case "reserved":
				inside := filepath.Join(home, ".tiler", "images")
				if err := os.MkdirAll(inside, 0o755); err != nil {
					t.Fatal(err)
				}
				chdir(t, inside)
			}
			for _, args := range [][]string{{"version"}, {"--version"}} {
				stdout, stderr, _, err := runCmd(t, args...)
				if err != nil || stdout != "tiler "+Version+"\n" || stderr != "" {
					t.Fatalf("%v: out %q, err %q, error %v", args, stdout, stderr, err)
				}
			}
			data, err := os.ReadFile(path)
			if content == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("version query created configuration: %q, %v", data, err)
				}
			} else if err != nil || string(data) != content {
				t.Fatalf("configuration changed: %q, %v", data, err)
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("version query created a config backup: %v", err)
			}
		})
	}
}
