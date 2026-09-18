package diagnostics

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var logRE = regexp.MustCompile(`^daemon(?:-[0-9]{6})?\.log(?:\.gz)?$`)

// Record is one daemon diagnostic log line, flattened for display.
type Record struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Text  string    `json:"text,omitempty"`
}

// Records returns the most recent limit diagnostic records, oldest first,
// reading rotated archives before the active log file.
// ponytail: loads whole log files into memory (max ~40 MiB with default rotation);
// stream a tail window if real-world log volumes ever grow that large.
func (m *Manager) Records(limit int) ([]Record, error) {
	limit = min(max(limit, 1), 2000)
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if logRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // daemon-NNNNNN.log.gz sorts before daemon.log: oldest first
	var records []Record
	for _, name := range names {
		f, err := os.Open(filepath.Join(m.directory, name))
		if err != nil {
			continue
		}
		var rd io.Reader = f
		if strings.HasSuffix(name, ".gz") {
			z, gzErr := gzip.NewReader(f)
			if gzErr != nil {
				_ = f.Close()
				continue
			}
			rd = z
		}
		records = append(records, parseRecords(rd)...)
		_ = f.Close()
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	return records, nil
}

func parseRecords(rd io.Reader) []Record {
	var out []Record
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 4*maxRecordBytes)
	for sc.Scan() {
		var raw map[string]any
		if json.Unmarshal(sc.Bytes(), &raw) != nil {
			continue
		}
		var r Record
		r.Time, _ = time.Parse(time.RFC3339Nano, recordString(raw["time"]))
		r.Level = strings.ToUpper(recordString(raw["level"]))
		r.Msg = recordString(raw["msg"])
		var attrs []string
		for k, v := range raw {
			switch k {
			case "time", "level", "msg":
			default:
				attrs = append(attrs, k+"="+recordString(v))
			}
		}
		sort.Strings(attrs)
		r.Text = strings.Join(attrs, " ")
		out = append(out, r)
	}
	return out
}

func recordString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
