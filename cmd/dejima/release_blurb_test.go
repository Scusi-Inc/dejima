package main

import (
	"strings"
	"testing"
)

func TestReleaseBlurb(t *testing.T) {
	cases := []struct {
		name  string
		notes string
		want  string // substring the blurb must contain ("" = blurb must be empty)
	}{
		{
			name:  "lead paragraph before a heading",
			notes: "Opening OpenClaw now reaches its web console, not a log stream.\n\n## The root bug\nblah blah",
			want:  "Opening OpenClaw now reaches its web console",
		},
		{
			name:  "skips a leading heading, takes the prose after",
			notes: "## What's new\n\nThe identity gate no longer demands a token when you have one.",
			want:  "The identity gate no longer demands a token",
		},
		{
			name:  "strips markdown emphasis and list leaders",
			notes: "- **Enter** on a keyless agent opens the `key editor`.",
			want:  "Enter on a keyless agent opens the key editor",
		},
		{name: "empty notes", notes: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := releaseBlurb(c.notes)
			if c.want == "" {
				if got != "" {
					t.Errorf("blurb = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("blurb = %q, want it to contain %q", got, c.want)
			}
			if strings.Contains(got, "**") || strings.Contains(got, "`") {
				t.Errorf("blurb still has markdown noise: %q", got)
			}
		})
	}
}

// The blurb is capped so a long lead paragraph can't blow out the confirm box.
func TestReleaseBlurbCapsLength(t *testing.T) {
	long := strings.Repeat("word ", 200)
	got := releaseBlurb(long)
	if len(got) > 245 {
		t.Errorf("blurb length = %d, want capped (~240 + ellipsis)", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated blurb should end with an ellipsis")
	}
}
