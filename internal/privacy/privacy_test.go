package privacy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRuntimeContainsNoLicenseOrTelemetryCode(t *testing.T) {
	root := projectRoot(t)
	patterns := map[string]*regexp.Regexp{
		"license enforcement": regexp.MustCompile(`(?i)\blicen[cs](?:e|ed|ing)?\b`),
		"telemetry":           regexp.MustCompile(`(?i)telemetr|analytics|host[_ -]?fingerprint|machine[_ -]?id|crash[_ -]?report`),
		"known validator":     regexp.MustCompile(`(?i)validator\.[a-z0-9.-]+`),
	}

	for _, directory := range []string{"cmd", "internal"} {
		walkProjectFiles(t, filepath.Join(root, directory), func(path string, contents []byte) {
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return
			}
			for name, pattern := range patterns {
				if match := pattern.Find(contents); match != nil {
					t.Errorf("%s contains forbidden %s marker %q", relative(root, path), name, match)
				}
			}
		})
	}
}

func TestInstallerContainsNoNetworkFetchOrLookup(t *testing.T) {
	root := projectRoot(t)
	path := filepath.Join(root, "installer", "backhaul.sh")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	patterns := map[string]*regexp.Regexp{
		"embedded network endpoint": regexp.MustCompile(`(?i)(?:https?|wss?)://[a-z0-9]`),
		"network download command":  regexp.MustCompile(`(?im)^\s*(?:sudo\s+)?(?:curl|wget|fetch)\b`),
		"online package manager":    regexp.MustCompile(`(?im)^\s*(?:sudo\s+)?(?:apt(?:-get)?|dnf|yum|apk|pacman)\b`),
		"public IP lookup":          regexp.MustCompile(`(?i)ipwhois|ipify|ifconfig\.me|icanhazip`),
	}
	for name, pattern := range patterns {
		if match := pattern.Find(contents); match != nil {
			t.Errorf("installer contains forbidden %s marker %q", name, match)
		}
	}
}

func TestExecutableSourcesContainNoHardCodedExternalURL(t *testing.T) {
	root := projectRoot(t)
	endpoint := regexp.MustCompile(`(?i)(?:https?|wss?)://[a-z0-9]`)
	for _, directory := range []string{"cmd", "internal", "installer", "examples"} {
		walkProjectFiles(t, filepath.Join(root, directory), func(path string, contents []byte) {
			if strings.HasSuffix(path, "_test.go") {
				return
			}
			if match := endpoint.Find(contents); match != nil {
				t.Errorf("%s contains hard-coded external URL marker %q", relative(root, path), match)
			}
		})
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func walkProjectFiles(t *testing.T, root string, inspect func(string, []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inspect(path, contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return value
}
