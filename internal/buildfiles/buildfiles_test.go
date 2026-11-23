package buildfiles

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMakefileTargetsDefaultAndDeps(t *testing.T) {
	m := ParseMakefile(fixture(t, "Makefile"))
	if want := []string{"default", "ios", "android", "test", "web"}; !reflect.DeepEqual(m.Targets, want) {
		t.Errorf("targets = %v, want %v: no variables, patterns, or special targets", m.Targets, want)
	}
	if m.Default != "default" {
		t.Errorf("default = %q: the first explicit target", m.Default)
	}
	want := map[string][]string{
		"default": {"ios"},
		"ios":     {},
		"android": {"gradle", "sdk"},
		"test":    {},
		"web":     {"build"},
	}
	if !reflect.DeepEqual(m.Deps, want) {
		t.Errorf("deps = %v, want %v", m.Deps, want)
	}
}

func TestMakefileDefaultGoalWins(t *testing.T) {
	m := ParseMakefile(fixture(t, "goal.mk"))
	if m.Default != "mac" || !reflect.DeepEqual(m.Deps["mac"], []string{"build"}) {
		t.Errorf("make = %+v", m)
	}
}

func TestWorkflowRunnersStepsAndActions(t *testing.T) {
	w, err := ParseWorkflow(".github/workflows/ci.yml", fixture(t, "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Jobs in name order: desktop, ios, release, test.
	if want := []string{"${{ matrix.os }}", "macos-15", "windows-latest", "self-hosted", "macOS", "${{ vars.RUNS_ON || 'house' }}"}; !reflect.DeepEqual(w.RunsOn, want) {
		t.Errorf("runs_on = %q, want %q", w.RunsOn, want)
	}
	if len(w.Runs) != 3 || w.Runs[1] != "xcodebuild archive -scheme App\n" {
		t.Errorf("runs = %q", w.Runs)
	}
	if want := []string{"apple-actions/upload-testflight-build@v1", "example/workflows/.github/workflows/release.yml@main", "actions/checkout@v4"}; !reflect.DeepEqual(w.Uses, want) {
		t.Errorf("uses = %q, want %q", w.Uses, want)
	}
}

func TestPlatformsFromProjectFiles(t *testing.T) {
	cases := []struct {
		path, fixture string
		want          []string
	}{
		{"ios/App.xcodeproj/project.pbxproj", "App.pbxproj", []string{IOS, Mac}},
		{"android/app/build.gradle.kts", "app.gradle.kts", []string{Android}},
		{"android/lib/build.gradle", "lib.gradle", nil},
		{"desktop/package.json", "electron.json", []string{Mac}},
		{"web/package.json", "web.json", nil},
		{"app/Win.csproj", "Win.csproj", []string{Windows}},
		{"cli/Lib.csproj", "Lib.csproj", nil},
	}
	for _, c := range cases {
		if got := Platforms(c.path, fixture(t, c.fixture)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestReadShapesTheWholeFact(t *testing.T) {
	files := map[string]string{
		"Makefile":                          "Makefile",
		".github/workflows/ci.yml":          "ci.yml",
		"ios/App.xcodeproj/project.pbxproj": "App.pbxproj",
		"desktop/package.json":              "electron.json",
		"src-tauri/tauri.conf.json":         "",
		"node_modules/x/package.json":       "",
		"internal/x/testdata/Win.csproj":    "",
		"web/fixtures/tauri.conf.json":      "",
		".github/workflows/scripts/x.yml":   "",
		"README.md":                         "",
	}
	var tree []string
	for f := range files {
		tree = append(tree, f)
	}
	var opened []string
	b, err := Read(tree, func(p string) ([]byte, error) {
		opened = append(opened, p)
		if files[p] == "" {
			t.Errorf("%s should not be opened", p)
			return nil, errors.New("not wanted")
		}
		return fixture(t, files[p]), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 4 || !b.Make.Present || len(b.Workflows) != 1 {
		t.Errorf("opened %v, build %+v", opened, b)
	}
	want := []App{{Mac, "desktop/package.json"}, {IOS, "ios/App.xcodeproj/project.pbxproj"}, {Mac, "ios/App.xcodeproj/project.pbxproj"}, {Mac, "src-tauri/tauri.conf.json"}}
	if !reflect.DeepEqual(b.Apps, want) {
		t.Errorf("apps = %+v, want %+v", b.Apps, want)
	}
}

func TestReadWithoutAMakefileSaysSo(t *testing.T) {
	b, err := Read([]string{"README.md"}, func(string) ([]byte, error) { return nil, errors.New("nothing to open") })
	if err != nil || b.Make.Present || b.Make.Targets == nil || b.Apps == nil || b.Workflows == nil {
		t.Errorf("an absent Makefile is known absent, with empty lists: %+v, %v", b, err)
	}
	if _, err := Read([]string{"Makefile"}, func(string) ([]byte, error) { return nil, errors.New("boom") }); err == nil {
		t.Error("a file that cannot be opened fails the fact")
	}
}
