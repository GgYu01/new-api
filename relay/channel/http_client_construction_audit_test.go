package channel

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// httpClientConstruction is one `&http.Client{` / `http.Client{` site under the
// G007-C003 audit packages (relay/, common/, service/, controller/, middleware/).
type httpClientConstruction struct {
	RelPath string
	Line    int
	Snippet string
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found from %s", thisFile)
		dir = parent
	}
}

func collectHTTPClientConstructions(t *testing.T, root string, packages []string) []httpClientConstruction {
	t.Helper()
	var out []httpClientConstruction
	for _, pkg := range packages {
		pkgRoot := filepath.Join(root, filepath.FromSlash(pkg))
		err := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				comp, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := comp.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "http" || sel.Sel.Name != "Client" {
					return true
				}
				pos := fset.Position(comp.Pos())
				out = append(out, httpClientConstruction{
					RelPath: filepath.ToSlash(rel),
					Line:    pos.Line,
					Snippet: constructionSnippet(path, pos.Line),
				})
				return true
			})
			return nil
		})
		require.NoError(t, err, "walk %s", pkg)
	}
	return out
}

func constructionSnippet(path string, line int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		if n == line {
			return strings.TrimSpace(sc.Text())
		}
	}
	return ""
}

func enclosingFuncName(path string, line int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	last := ""
	for sc.Scan() {
		n++
		trim := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(trim, "func ") {
			name := strings.TrimPrefix(trim, "func ")
			name = strings.SplitN(name, "(", 2)[0]
			name = strings.TrimSpace(name)
			if parts := strings.Fields(name); len(parts) > 0 {
				last = parts[len(parts)-1]
				last = strings.Trim(last, "*")
			}
		}
		if n == line {
			return last
		}
	}
	return last
}

func hasTimeoutLiteral(snippet string) bool {
	return strings.Contains(snippet, "Timeout:")
}

// Test_HTTPClientConstructions_relayVsAdminOneShots is the G007-C003 audit of
// every `&http.Client{` / `http.Client{` construction under relay/, common/,
// service/, controller/, and middleware/.
//
// Classification:
//
//	relay-traffic: must use the shared service client (Timeout==0 unless
//	RELAY_TIMEOUT is explicitly set) plus phase contexts from
//	relay/lifecycle.LogicalRequest. An absolute Client.Timeout here would
//	cap a 60m logical request.
//
//	admin/ops one-shot: console, OAuth, payment, model-sync, uptime, and
//	Ollama model-pull paths. These never serve a 60-minute customer SSE
//	request, so a short/long Client.Timeout is a per-call bound, not a
//	relay-lifetime leak. Justified below.
func Test_HTTPClientConstructions_relayVsAdminOneShots(t *testing.T) {
	root := moduleRoot(t)
	got := collectHTTPClientConstructions(t, root, []string{
		"relay", "common", "service", "controller", "middleware",
	})
	require.NotEmpty(t, got)

	type class string
	const (
		classRelayShared class = "relay-shared"
		classAdmin       class = "admin-ops"
	)
	type expected struct {
		class   class
		timeout string
		note    string
	}

	// Exact inventory. Adding a new construction without classifying it fails
	// this test on purpose (RED for the next reviewer).
	want := map[string]expected{
		// Shared relay factory. Timeout stays 0 unless RELAY_TIMEOUT is set
		// (operational override; documented in service/relay_client_timeout_test.go).
		"service/http_client.go": {
			class:   classRelayShared,
			timeout: "RELAY_TIMEOUT-or-0",
			note:    "newRelayHTTPClient: shared GetHttpClient / GetHttpClientWithProxySettings factory used by doRequest and provider adaptors",
		},
		// SSRF-protected fetch used by Midjourney image proxy and other
		// user-URL fetches. Same RELAY_TIMEOUT override; default Timeout==0.
		"service/protected_fetch_client.go": {
			class:   classRelayShared,
			timeout: "RELAY_TIMEOUT-or-0",
			note:    "GetSSRFProtectedHTTPClient; not an absolute 60m cap by default",
		},
		// Codex OAuth token refresh. Copies the shared client then stamps a
		// 20s Timeout on the copy. Admin credential refresh, not customer SSE.
		"service/codex_oauth.go": {
			class:   classAdmin,
			timeout: "20s",
			note:    "getCodexOAuthHTTPClient: OAuth token refresh one-shot; copies shared client then sets defaultHTTPTimeout=20s. Cannot fire on a 60m logical request because it is not on the relay data path.",
		},

		// Ollama admin/ops one-shots (controller/channel.go model pull/list/version).
		// These are console model-management paths, not customer /v1 relay traffic.
		// FetchOllamaModels: no Timeout (operator waits for tags listing).
		// PullOllamaModel: 30m — large model download, admin path.
		// PullOllamaModelStream: 60m — streaming pull of huge models, admin path.
		// FetchOllamaVersion: 10s — version probe.
		// DeleteOllamaModel: no Timeout — small DELETE.
		// None of these can fire on a 60m customer logical request.
		"relay/channel/ollama/relay-ollama.go:FetchOllamaModels": {
			class:   classAdmin,
			timeout: "0",
			note:    "FetchOllamaModels admin tags listing; no Timeout. Console channel-update path, not /v1 relay.",
		},
		"relay/channel/ollama/relay-ollama.go:PullOllamaModel": {
			class:   classAdmin,
			timeout: "30m",
			note:    "PullOllamaModel 30m absolute Timeout for large-model download. Admin pull from controller/channel.go, not customer relay.",
		},
		"relay/channel/ollama/relay-ollama.go:PullOllamaModelStream": {
			class:   classAdmin,
			timeout: "60m",
			note:    "PullOllamaModelStream 60m absolute Timeout for huge-model streaming pull. Admin path; even if it equals the logical lifetime it is not a customer SSE client.",
		},
		"relay/channel/ollama/relay-ollama.go:DeleteOllamaModel": {
			class:   classAdmin,
			timeout: "0",
			note:    "DeleteOllamaModel admin DELETE; no Timeout.",
		},
		"relay/channel/ollama/relay-ollama.go:FetchOllamaVersion": {
			class:   classAdmin,
			timeout: "10s",
			note:    "FetchOllamaVersion 10s version probe. Admin/ops one-shot.",
		},

		// Console / ops one-shots. None sit on /v1 relay traffic.
		// Ali image updateTask is customer relay traffic and MUST NOT appear
		// here: it has to reuse GetHttpClientWithProxySettings (see
		// relay/channel/ali/image_client_test.go). A leftover `&http.Client{}`
		// on that path is a G007-C003 failure.
		"controller/wechat.go": {
			class:   classAdmin,
			timeout: "5s",
			note:    "getWeChatIdByCode: WeChat login exchange. Auth one-shot.",
		},
		"controller/ratio_sync.go": {
			class:   classAdmin,
			timeout: "0+transport",
			note:    "FetchUpstreamRatios: admin pricing sync. Transport has 10s ResponseHeaderTimeout; per-request context.WithTimeout. Not relay traffic.",
		},
		"controller/model_sync.go": {
			class:   classAdmin,
			timeout: "0+transport",
			note:    "newHTTPClient: admin model-metadata sync from github.io. Transport timeouts only; Client.Timeout unset.",
		},
		"controller/topup_creem.go": {
			class:   classAdmin,
			timeout: "30s",
			note:    "genCreemLink: payment checkout create. Billing one-shot.",
		},
		"controller/custom_oauth.go": {
			class:   classAdmin,
			timeout: "20s",
			note:    "FetchCustomOAuthDiscovery: admin OIDC discovery fetch.",
		},
		"controller/uptime_kuma.go": {
			class:   classAdmin,
			timeout: "10s",
			note:    "GetUptimeKumaStatus: console status-page fetch, httpTimeout=10s.",
		},
	}

	seen := map[string]int{}
	for _, site := range got {
		key := site.RelPath
		if site.RelPath == "relay/channel/ollama/relay-ollama.go" {
			fn := enclosingFuncName(filepath.Join(root, filepath.FromSlash(site.RelPath)), site.Line)
			switch fn {
			case "FetchOllamaVersion", "PullOllamaModel", "PullOllamaModelStream", "FetchOllamaModels", "DeleteOllamaModel":
				key = "relay/channel/ollama/relay-ollama.go:" + fn
			default:
				t.Fatalf("ollama http.Client in unknown func %q at line %d", fn, site.Line)
			}
		}
		exp, ok := want[key]
		require.True(t, ok, "unclassified http.Client construction at %s:%d %q — classify as relay-traffic (must use shared client / phase context) or admin/ops", site.RelPath, site.Line, site.Snippet)
		seen[key]++

		switch exp.class {
		case classRelayShared:
			if exp.timeout == "0" {
				require.False(t, hasTimeoutLiteral(site.Snippet) && !strings.Contains(site.Snippet, "Timeout: 0"),
					"%s:%d relay-traffic construction must not set a non-zero Timeout literal: %q (%s)",
					site.RelPath, site.Line, site.Snippet, exp.note)
			}
		case classAdmin:
			// Admin/ops may set Timeout. Presence is recorded, not forbidden.
		}
		_ = exp
	}

	for key := range want {
		require.Greater(t, seen[key], 0, "expected construction %s missing from source; inventory stale", key)
	}
}
