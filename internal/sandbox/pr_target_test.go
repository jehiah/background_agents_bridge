package sandbox

import (
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

func TestResolveRepositoryTargetManifest(t *testing.T) {
	repos := []repomanifest.Entry{
		{Owner: "OctoCat", Name: "Hello", Path: "/workspace/Hello"},
		{Owner: "group/sub", Name: "world", Path: "/workspace/world"},
	}

	// Case-insensitive match returns canonical casing + path.
	got := resolveRepositoryTarget("octocat/hello", repos)
	if got == nil || got.owner != "OctoCat" || got.name != "Hello" || got.path != "/workspace/Hello" {
		t.Fatalf("canonical match = %+v", got)
	}

	// Nested owner preserved.
	got = resolveRepositoryTarget("group/sub/world", repos)
	if got == nil || got.owner != "group/sub" || got.name != "world" || got.path != "/workspace/world" {
		t.Fatalf("nested match = %+v", got)
	}

	// Not a session member.
	if got := resolveRepositoryTarget("other/repo", repos); got != nil {
		t.Errorf("expected nil for non-member, got %+v", got)
	}
}

func TestResolveRepositoryTargetNoManifest(t *testing.T) {
	cases := []struct {
		in          string
		wantOwner   string
		wantName    string
		wantNilArgs bool
	}{
		{"octocat/hello", "octocat", "hello", false},
		{"group/sub/project", "group/sub", "project", false}, // split on last "/"
		{"noslash", "", "", true},
		{"/leading", "", "", true},
		{"trailing/", "", "", true},
		{"a//b", "", "", true}, // empty owner segment
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := resolveRepositoryTarget(tc.in, nil)
			if tc.wantNilArgs {
				if got != nil {
					t.Errorf("want nil, got %+v", got)
				}
				return
			}
			if got == nil || got.owner != tc.wantOwner || got.name != tc.wantName || got.path != "" {
				t.Errorf("got %+v, want owner=%q name=%q path=\"\"", got, tc.wantOwner, tc.wantName)
			}
		})
	}
}
