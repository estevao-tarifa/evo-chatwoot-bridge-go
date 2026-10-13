package httpapi

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// openAPIPathKey matches a 2-space-indented path key, e.g. "  /api/v1/logs:".
var openAPIPathKey = regexp.MustCompile(`^  (/[^:]+):\s*$`)

// routeKey identifies one (method, path) pair.
type routeKey struct {
	method string
	path   string
}

func (k routeKey) String() string { return k.method + " " + k.path }

// parseOpenAPIRoutes scans a hand-written OpenAPI YAML file for (method, path)
// pairs. It is deliberately small and format-specific (2-space path keys,
// 4-space method keys); an unknown method, an unknown path-item key or a
// malformed line fails loudly so the spec can never silently drift. It is
// unit-tested in TestParseOpenAPIRoutes.
func parseOpenAPIRoutes(spec string) (map[routeKey]bool, error) {
	routes := map[routeKey]bool{}
	currentPath := ""
	scanner := bufio.NewScanner(strings.NewReader(spec))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if m := openAPIPathKey.FindStringSubmatch(line); m != nil {
			currentPath = m[1]
			continue
		}
		// A 2-space key that is not a path (e.g. a top-level "components:")
		// closes the current path block, so keys nested under it are not
		// mistaken for path items.
		if isTwoSpaceKey(line) {
			currentPath = ""
			continue
		}
		// A 4-space key inside a path block is a path item: only HTTP verbs and
		// the standard OpenAPI path-item keys are allowed. An unknown key (a
		// typo, or a method the bridge does not support) is a malformed spec.
		if key, ok := fourSpaceKey(line); ok {
			switch {
			case isSupportedMethod(key):
				if currentPath == "" {
					return nil, fmt.Errorf("line %d: method %q with no enclosing path", lineNo, key)
				}
				routes[routeKey{method: strings.ToUpper(key), path: currentPath}] = true
			case isHTTPVerb(key):
				return nil, fmt.Errorf("line %d: unsupported method %q", lineNo, key)
			case currentPath != "" && !isPathItemKey(key):
				return nil, fmt.Errorf("line %d: unknown path item key %q", lineNo, key)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("no routes found in spec")
	}
	return routes, nil
}

// isTwoSpaceKey reports whether line is a 2-space-indented "key:" line.
func isTwoSpaceKey(line string) bool {
	return strings.HasPrefix(line, "  ") &&
		!strings.HasPrefix(line, "    ") &&
		strings.HasSuffix(strings.TrimSpace(line), ":")
}

// fourSpaceKey returns the key of a 4-space-indented "key:" line.
func fourSpaceKey(line string) (string, bool) {
	if !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "      ") {
		return "", false
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasSuffix(trimmed, ":") {
		return "", false
	}
	return strings.TrimSuffix(trimmed, ":"), true
}

func isHTTPVerb(key string) bool {
	switch key {
	case "get", "post", "patch", "delete", "put", "head", "options", "trace", "connect":
		return true
	default:
		return false
	}
}

// isSupportedMethod reports whether the bridge routes this method.
func isSupportedMethod(key string) bool {
	switch key {
	case "get", "post", "patch", "delete", "put":
		return true
	default:
		return false
	}
}

// isPathItemKey reports whether key is a standard OpenAPI path-item field
// (allowed alongside the HTTP verbs).
func isPathItemKey(key string) bool {
	switch key {
	case "parameters", "summary", "description", "servers", "$ref":
		return true
	default:
		return false
	}
}

func loadOpenAPIRoutes(t *testing.T) map[routeKey]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	specPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	routes, err := parseOpenAPIRoutes(string(data))
	if err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	return routes
}

// TestOpenAPIContractCoverage asserts the bidirectional lockstep between the Go
// route table and backend/api/openapi.yaml (spec §2, A13). The scanner and the
// router read the same apiRoutes table, so they cannot drift by construction.
func TestOpenAPIContractCoverage(t *testing.T) {
	spec := loadOpenAPIRoutes(t)

	registered := map[routeKey]bool{}
	for _, rt := range apiRoutes {
		registered[routeKey{method: rt.Method, path: rt.Pattern}] = true
	}

	for key := range registered {
		if !spec[key] {
			t.Errorf("route %s is registered but missing from openapi.yaml", key)
		}
	}
	for key := range spec {
		if !registered[key] {
			t.Errorf("openapi.yaml declares %s but it is not registered", key)
		}
	}
}

// TestParseOpenAPIRoutes is the unit test for the scanner itself.
func TestParseOpenAPIRoutes(t *testing.T) {
	t.Run("extracts method and path pairs", func(t *testing.T) {
		spec := "openapi: 3.0.3\npaths:\n  /api/v1/logs:\n    get:\n      summary: x\n  /api/v1/connections/{id}:\n    patch:\n      summary: y\n    delete:\n      summary: z\n"
		routes, err := parseOpenAPIRoutes(spec)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, want := range []routeKey{
			{http.MethodGet, "/api/v1/logs"},
			{http.MethodPatch, "/api/v1/connections/{id}"},
			{http.MethodDelete, "/api/v1/connections/{id}"},
		} {
			if !routes[want] {
				t.Errorf("missing %s", want)
			}
		}
		if len(routes) != 3 {
			t.Errorf("routes = %d, want 3", len(routes))
		}
	})

	t.Run("rejects an unsupported method", func(t *testing.T) {
		spec := "paths:\n  /x:\n    head:\n      summary: x\n    get:\n      summary: y\n"
		if _, err := parseOpenAPIRoutes(spec); err == nil {
			t.Error("expected an error for an unsupported method")
		}
	})

	t.Run("rejects an unknown non-verb key under a path", func(t *testing.T) {
		spec := "paths:\n  /x:\n    fetch:\n      summary: x\n    get:\n      summary: y\n"
		if _, err := parseOpenAPIRoutes(spec); err == nil {
			t.Error("expected an error for an unknown path item key")
		}
	})

	t.Run("allows standard path-item keys", func(t *testing.T) {
		spec := "paths:\n  /x:\n    parameters:\n      - name: q\n    get:\n      summary: y\n"
		routes, err := parseOpenAPIRoutes(spec)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !routes[routeKey{http.MethodGet, "/x"}] {
			t.Error("missing GET /x")
		}
	})

	t.Run("rejects an empty spec", func(t *testing.T) {
		if _, err := parseOpenAPIRoutes("openapi: 3.0.3\n"); err == nil {
			t.Error("expected an error for a spec with no routes")
		}
	})
}
