package managednpm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareNPMProjectPrefixUsesPrivateTemporaryRoot(t *testing.T) {
	tempRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("TEMP", tempRoot)
		t.Setenv("TMP", tempRoot)
	} else {
		t.Setenv("TMPDIR", tempRoot)
	}
	agentHome := filepath.Join(t.TempDir(), "mounted-agent-home")
	if err := os.MkdirAll(agentHome, 0o700); err != nil {
		t.Fatalf("create mounted agent home: %v", err)
	}

	args := append([]string{"npx"}, NPMProjectPrefixArgs()...)
	if err := PrepareNPMProjectPrefix(args); err != nil {
		t.Fatalf("prepare npm project prefix: %v", err)
	}

	got := args[2]
	if !filepath.IsAbs(got) || !strings.HasPrefix(got, filepath.Clean(tempRoot)+string(filepath.Separator)) {
		t.Fatalf("npm project prefix = %q, want absolute path under system temp root %q", got, tempRoot)
	}
	var identity string
	if runtime.GOOS == "windows" {
		current, err := user.Current()
		if err != nil || current == nil {
			t.Fatalf("current Windows user = (%v, %v)", current, err)
		}
		identity = current.Uid
	} else {
		identity = strconv.Itoa(os.Getuid())
	}
	userHash := sha256.Sum256([]byte(identity))
	want := filepath.Join(tempRoot, "kandev-managed-npm-runtime-"+hex.EncodeToString(userHash[:8]))
	if got != want {
		t.Fatalf("npm project prefix = %q, want identity-scoped path %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("npm project prefix stat = (%v, %v), want an existing directory", info, err)
	}
	if runtime.GOOS != "windows" {
		if gotMode := info.Mode().Perm(); gotMode != 0o700 {
			t.Fatalf("npm project prefix permissions = %#o, want %#o", gotMode, 0o700)
		}
	}

	legacyHomePrefix := filepath.Join(agentHome, ".kandev", "managed-npm-runtime")
	if _, err := os.Stat(legacyHomePrefix); !os.IsNotExist(err) {
		t.Fatalf("npm project prefix was created under mounted agent home, stat error = %v", err)
	}
}
