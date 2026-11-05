package center

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactHistoryPreservesRetainedLineBytes(t *testing.T) {
	const valid1 = `{"value":1}`
	const valid2 = `  {"value":2}`
	const valid3 = `{"value":3}  `
	cases := []struct {
		name       string
		input      string
		maxEntries int
		maxBytes   int64
		want       string
		count      int
	}{
		{"unchanged keeps original newlines", valid1 + "\r\n" + valid2, 3, 128, valid1 + "\r\n" + valid2, 2},
		{"entry limit and invalid gaps", valid1 + "\r\n" + valid2 + "\r\ninvalid\r\n" + valid3, 2, 128, valid2 + "\n" + valid3 + "\n", 2},
		{"byte limit and oversized record", valid1 + "\n" + `"` + strings.Repeat("x", 50) + `"` + "\n" + valid2 + "\r\n" + valid3, 5, int64(len(valid2) + len(valid3) + 2), valid2 + "\n" + valid3 + "\n", 2},
		{"no valid records", "\r\ninvalid", 2, 128, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.ndjson")
			if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
				t.Fatal(err)
			}
			count, err := compactHistoryFile(path, tc.maxEntries, tc.maxBytes)
			if err != nil || count != tc.count {
				t.Fatalf("compaction count = %d, error = %v, want %d", count, err, tc.count)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("compacted data = %q, error = %v, want %q", got, err, tc.want)
			}
		})
	}
}

func BenchmarkCompactHistoryC2(b *testing.B) {
	const retained = 2000
	line := []byte(`{"node_id":"synthetic","inventory":{"fixture":"` + strings.Repeat("x", 23250) + `"}}` + "\n")
	path := filepath.Join(b.TempDir(), "synthetic.ndjson")
	b.ReportAllocs()
	b.SetBytes(int64(len(line) * (retained + 1)))
	for b.Loop() {
		b.StopTimer()
		file, err := os.Create(path)
		if err != nil {
			b.Fatal(err)
		}
		writer := bufio.NewWriterSize(file, 64<<10)
		for row := 0; row <= retained; row++ {
			if _, err := writer.Write(line); err != nil {
				b.Fatal(err)
			}
		}
		if err := writer.Flush(); err != nil {
			b.Fatal(err)
		}
		if err := file.Close(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		got, err := compactHistoryFile(path, retained, 64<<20)
		if got != retained || err != nil {
			b.Fatalf("compactHistoryFile = %d, %v", got, err)
		}
	}
}
