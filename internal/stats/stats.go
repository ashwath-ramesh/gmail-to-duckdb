package stats

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.sql
var files embed.FS

type Query struct {
	ID   string
	Name string
	SQL  string
}

func All() ([]Query, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Query
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		q, err := parse(e.Name(), string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func Get(id string) (Query, error) {
	qs, err := All()
	if err != nil {
		return Query{}, err
	}
	for _, q := range qs {
		if q.ID == id {
			return q, nil
		}
	}
	return Query{}, fmt.Errorf("unknown stat %q", id)
}

func parse(filename, raw string) (Query, error) {
	name := strings.TrimSuffix(filename, ".sql")
	sql := strings.TrimSpace(raw)
	if strings.HasPrefix(sql, "--") {
		line, rest, _ := strings.Cut(sql, "\n")
		if n, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(line, "--")), "name:"); ok {
			name = strings.TrimSpace(n)
			sql = strings.TrimSpace(rest)
		}
	}
	if !strings.HasPrefix(strings.ToUpper(sql), "SELECT") {
		return Query{}, fmt.Errorf("%s: only SELECT stats are allowed", filename)
	}
	id := strings.TrimSuffix(filename, ".sql")
	return Query{ID: id, Name: name, SQL: sql}, nil
}
