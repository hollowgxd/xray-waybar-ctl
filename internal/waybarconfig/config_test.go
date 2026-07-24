package waybarconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchMultilineJSONCPreservesCommentsAndIsIdempotent(t *testing.T) {
	src := []byte(`{
  // keep this comment
  "modules-right": [
    "clock",
  ],
  "clock": {
    "format": "{:%H:%M}",
  },
}
`)
	first, changed, err := Patch(src, "/home/test/.local/bin/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first patch reported unchanged")
	}
	text := string(first)
	for _, want := range []string{
		"// keep this comment",
		`"custom/vpn",`,
		`"custom/vpn": {`,
		`"/home/test/.local/bin/xray-waybar-ctl status"`,
		`"clock": {`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("patched config does not contain %q:\n%s", want, text)
		}
	}

	second, changed, err := Patch(first, "/home/test/.local/bin/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second patch must be idempotent")
	}
	if string(second) != string(first) {
		t.Fatal("idempotent patch changed bytes")
	}
}

func TestPatchKeepsExistingCustomizationAndPlacement(t *testing.T) {
	src := []byte(`{
  "modules-left": ["custom/vpn"],
  "modules-right": ["clock"],
  "custom/vpn": {
    "exec": "my-own-script",
    "interval": 42
  }
}`)
	out, changed, err := Patch(src, "/ignored/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("customized existing module should not be changed")
	}
	if string(out) != string(src) {
		t.Fatal("existing customization was rewritten")
	}
}

func TestPatchTopLevelArraySelectsObjectWithModulesRight(t *testing.T) {
	src := []byte(`[
  {"name": "left-only", "modules-left": ["clock"]},
  {
    "name": "target",
    "modules-right": ["tray"]
  }
]`)
	out, changed, err := Patch(src, "/bin/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("patch reported unchanged")
	}
	text := string(out)
	target := strings.Index(text, `"name": "target"`)
	module := strings.Index(text, `"custom/vpn": {`)
	if target < 0 || module < target {
		t.Fatalf("module was not added to target object:\n%s", text)
	}
}

func TestPatchAddsMissingPositionArray(t *testing.T) {
	src := []byte(`{
  "position": "top"
}`)
	out, changed, err := Patch(src, "/bin/xray-waybar-ctl", "center")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("patch reported unchanged")
	}
	for _, want := range []string{`"modules-center": ["custom/vpn"]`, `"custom/vpn": {`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestPatchAcceptsInlineTrailingComma(t *testing.T) {
	src := []byte(`{"position":"top",}`)
	out, changed, err := Patch(src, "/bin/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("patch reported unchanged")
	}
	if strings.Contains(string(out), ",,") {
		t.Fatalf("patch created a double comma: %s", out)
	}
	if _, _, err := Patch(out, "/bin/xray-waybar-ctl", "right"); err != nil {
		t.Fatalf("patched output is not parseable: %v\n%s", err, out)
	}
}

func TestPatchRejectsMalformedJSONC(t *testing.T) {
	if _, _, err := Patch([]byte(`{"modules-right": [`), "/bin/ctl", "right"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestInstallCreatesBackupAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	src := []byte("{\n  \"modules-right\": [\"clock\"]\n}\n")
	if err := os.WriteFile(path, src, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := Install(path, "/bin/xray-waybar-ctl", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.BackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(src) {
		t.Fatal("backup differs from original")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}
