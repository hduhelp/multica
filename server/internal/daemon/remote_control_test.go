package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLogFileContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "daemon.log")
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Last 5 lines.
	out, err := tailLogFileContent(p, 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Count(out, "\n") + 1
	if got != 5 {
		t.Errorf("expected 5 lines, got %d: %q", got, out)
	}
	// More than the file has → all 100.
	out, _ = tailLogFileContent(p, 1000, 1<<20)
	if n := strings.Count(out, "\n") + 1; n != 100 {
		t.Errorf("expected 100 lines, got %d", n)
	}
	// Byte cap trims from the front.
	capped, _ := tailLogFileContent(p, 1000, 20)
	if len(capped) > 40 {
		t.Errorf("cap not honored: %d bytes", len(capped))
	}
	// Missing file errors.
	if _, err := tailLogFileContent(filepath.Join(dir, "nope.log"), 5, 1<<20); err == nil {
		t.Error("expected error for missing file")
	}
}
