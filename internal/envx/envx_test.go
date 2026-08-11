package envx_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/envx"
)

func TestSanitizePATH(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"strips rbenv concrete version bin, keeps shims",
			"/home/u/.rbenv/versions/3.3.9/bin:/home/u/.rbenv/shims:/usr/bin",
			"/home/u/.rbenv/shims:/usr/bin",
		},
		{
			"strips asdf install bin",
			"/home/u/.asdf/installs/ruby/3.4.8/bin:/usr/bin",
			"/usr/bin",
		},
		{
			"keeps unrelated dirs",
			"/usr/local/bin:/usr/bin:/bin",
			"/usr/local/bin:/usr/bin:/bin",
		},
		{
			"trailing slash still stripped",
			"/home/u/.rbenv/versions/3.3.9/bin/:/usr/bin",
			"/usr/bin",
		},
		{"empty", "", ""},
		{
			"versions dir without bin suffix survives",
			"/home/u/.rbenv/versions/3.3.9/libexec:/usr/bin",
			"/home/u/.rbenv/versions/3.3.9/libexec:/usr/bin",
		},
		{
			"empty segments survive untouched",
			"/usr/bin::/bin",
			"/usr/bin::/bin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envx.SanitizePATH(tc.in); got != tc.want {
				t.Errorf("SanitizePATH(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCurated(t *testing.T) {
	parent := []string{
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"RBENV_VERSION=3.3.9", // version-manager pin: dropped
		"SECRET_TOKEN=shh",    // not allowlisted: dropped
		"MY_TOKEN=ok",         // allowlisted via extraAllow
		"PATH=/home/u/.rbenv/versions/3.3.9/bin:/home/u/.rbenv/shims:/usr/bin",
	}
	overlay := map[string]string{"DB_NAME": "app_T1_development", "LANG": "C"}
	got := envx.Curated(parent, []string{"MY_TOKEN"}, overlay)

	want := map[string]string{
		"HOME":     "/home/u",
		"LANG":     "C", // overlay wins over parent
		"MY_TOKEN": "ok",
		"DB_NAME":  "app_T1_development",
		"PATH":     "/home/u/.rbenv/shims:/usr/bin",
	}
	gotMap := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		gotMap[k] = v
	}
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("%s = %q, want %q", k, gotMap[k], v)
		}
	}
	for _, banned := range []string{"RBENV_VERSION", "SECRET_TOKEN"} {
		if _, ok := gotMap[banned]; ok {
			t.Errorf("%s must not propagate", banned)
		}
	}
	if !slices.IsSorted(got) {
		t.Error("Curated output should be sorted for deterministic behavior")
	}
}

// An extraAllow entry naming a version-manager pin var exactly is an explicit
// user allowance (spec §6 escape hatch: "the documented answer when a needed
// var doesn't propagate") — it must win over the prefix drop, which exists to
// strip vars the user never asked for.
func TestCuratedExtraAllowBeatsPrefixDrop(t *testing.T) {
	parent := []string{
		"RBENV_VERSION=3.3.9",
		"RBENV_DIR=/home/u/app", // same prefix, NOT named: still dropped
	}
	got := envx.Curated(parent, []string{"RBENV_VERSION"}, nil)
	if !slices.Contains(got, "RBENV_VERSION=3.3.9") {
		t.Errorf("explicit extraAllow RBENV_VERSION must propagate; got %v", got)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "RBENV_DIR=") {
			t.Errorf("RBENV_DIR was not named in extraAllow and must stay dropped; got %v", got)
		}
	}
}

// PATH is always sanitized, even when named in extraAllow — sanitization is
// the tool's semantic core, not an allowlist question.
func TestCuratedPATHAlwaysSanitized(t *testing.T) {
	parent := []string{"PATH=/home/u/.rbenv/versions/3.3.9/bin:/usr/bin"}
	got := envx.Curated(parent, []string{"PATH"}, nil)
	if !slices.Contains(got, "PATH=/usr/bin") {
		t.Errorf("PATH must be sanitized regardless of extraAllow; got %v", got)
	}
}

func TestCuratedParentEdgeEntries(t *testing.T) {
	parent := []string{
		"HOME",    // no "=": malformed, dropped
		"=orphan", // empty key: dropped
		"LANG=",   // empty value: kept as empty
		"TZ=a=b",  // value containing "=": kept whole
		"PATH=",   // empty PATH: kept as empty
	}
	got := envx.Curated(parent, nil, nil)
	want := []string{"LANG=", "PATH=", "TZ=a=b"}
	if !slices.Equal(got, want) {
		t.Errorf("Curated = %v, want %v", got, want)
	}
}

func TestCuratedOverlayAloneSuffices(t *testing.T) {
	got := envx.Curated(nil, nil, map[string]string{"ONLY": "one"})
	if !slices.Equal(got, []string{"ONLY=one"}) {
		t.Errorf("Curated = %v, want [ONLY=one]", got)
	}
}

func TestSanitizeSelf(t *testing.T) {
	t.Setenv("PATH", "/home/u/.rbenv/versions/3.3.9/bin:/usr/bin")
	t.Setenv("RBENV_VERSION", "3.3.9")
	t.Setenv("MISE_RUBY_VERSION", "3.4.8")
	t.Setenv("HOME", os.Getenv("HOME")) // untouched control

	envx.SanitizeSelf()

	if got := os.Getenv("PATH"); got != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", got)
	}
	for _, k := range []string{"RBENV_VERSION", "MISE_RUBY_VERSION"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s must be deleted by SanitizeSelf", k)
		}
	}
}

func TestSanitizeSelfIdempotent(t *testing.T) {
	t.Setenv("PATH", "/home/u/.rbenv/versions/3.3.9/bin:/home/u/.rbenv/shims:/usr/bin")
	t.Setenv("ASDF_RUBY_VERSION", "3.4.8")

	envx.SanitizeSelf()
	first := os.Getenv("PATH")
	envx.SanitizeSelf()
	if got := os.Getenv("PATH"); got != first {
		t.Errorf("second SanitizeSelf changed PATH: %q -> %q", first, got)
	}
	if got := os.Getenv("PATH"); got != "/home/u/.rbenv/shims:/usr/bin" {
		t.Errorf("PATH = %q, want /home/u/.rbenv/shims:/usr/bin", got)
	}
}
