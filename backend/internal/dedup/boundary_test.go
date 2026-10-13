package dedup

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestImportBoundary enforces ARCHITECTURE §2 rules 2–3 at build time: the
// domain packages must not import SQL, transport, or the concrete adapters.
//
// The architecture lists a golangci-lint `depguard` rule, but the backend
// pipeline has no golangci-lint step (only gofmt/vet/staticcheck/govulncheck).
// A stdlib-only import test is the cheaper equivalent and fails the same way: a
// violating import breaks `go test ./...` in the same commit.
func TestImportBoundary(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	pkgDir := filepath.Dir(thisFile)

	tests := []struct {
		name      string
		dir       string
		forbidden []string
	}{
		{
			name: "dedup",
			dir:  pkgDir,
			// ARCHITECTURE §2 rule 2/3: no SQL, no transport, no adapters, and
			// never `bridge` (the domain graph is bridge → dedup).
			forbidden: []string{
				"database/sql", "net/http",
				"internal/storage", "internal/evolution", "internal/chatwoot",
				"internal/httpapi", "internal/bridge",
			},
		},
		{
			name: "bridge",
			dir:  filepath.Join(pkgDir, "..", "bridge"),
			// ARCHITECTURE §2 rule 2/3.
			forbidden: []string{
				"database/sql", "net/http",
				"internal/storage", "internal/evolution", "internal/chatwoot",
				"internal/httpapi",
			},
		},
		{
			name: "worker",
			dir:  filepath.Join(pkgDir, "..", "worker"),
			// ARCHITECTURE §2 rule 4: the worker moves opaque jobs and must
			// import no transport, SQL, client or domain package.
			forbidden: []string{
				"database/sql", "net/http",
				"internal/storage", "internal/evolution", "internal/chatwoot",
				"internal/httpapi", "internal/bridge", "internal/dedup",
			},
		},
		{
			name: "httpapi",
			dir:  filepath.Join(pkgDir, "..", "httpapi"),
			// ARCHITECTURE §2: the transport declares consumer-side ports and
			// must not import the concrete adapters (evolution, chatwoot,
			// storage/mysql). net/http is the transport's own package.
			forbidden: []string{
				"internal/storage", "internal/evolution", "internal/chatwoot",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, imported := range packageImports(t, tt.dir) {
				for _, bad := range tt.forbidden {
					if importMatches(imported, bad) {
						t.Errorf("package %s must not import %q (ARCHITECTURE §2)", tt.name, imported)
					}
				}
			}
		})
	}
}

// importMatches reports whether imported is path or a subpackage of path, e.g.
// ("github.com/x/backend/internal/storage/mysql", "internal/storage") → true.
func importMatches(imported, path string) bool {
	return imported == path ||
		strings.HasSuffix(imported, "/"+path) ||
		strings.Contains(imported, "/"+path+"/")
}

// packageImports parses the import declarations of every .go file in dir. It
// uses go/parser rather than go/build so it needs no module resolution and
// works offline.
func packageImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var imports []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", entry.Name(), err)
			}
			imports = append(imports, path)
		}
	}
	return imports
}
