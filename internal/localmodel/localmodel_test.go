package localmodel

import "testing"

func TestRecommendForBucketsByRAM(t *testing.T) {
	cases := []struct {
		ramGiB  int
		wantTop string // expected top-pick alias ("" = nothing fits)
	}{
		{ramGiB: 6, wantTop: ""},               // below the smallest (needs 8)
		{ramGiB: 8, wantTop: "qwen-coder-3b"},  // the 3B
		{ramGiB: 16, wantTop: "qwen-coder-7b"}, // the 7B
		{ramGiB: 40, wantTop: "qwen-coder"},    // 32B fits (36), 70B (48) doesn't
		{ramGiB: 64, wantTop: "llama-70b"},     // the 70B (largest coder that fits)
		{ramGiB: 512, wantTop: "kimi-k2"},      // fits the big MoE
	}
	for _, c := range cases {
		rec := RecommendFor(c.ramGiB)
		got := ""
		if rec.Top != nil {
			got = rec.Top.Alias
		}
		if got != c.wantTop {
			t.Errorf("RecommendFor(%d): top=%q, want %q (fits=%d)", c.ramGiB, got, c.wantTop, len(rec.Fits))
		}
	}
}

func TestRecommendForFitsAreLargestFirst(t *testing.T) {
	rec := RecommendFor(64)
	if len(rec.Fits) < 2 {
		t.Fatalf("expected several fits at 64GiB, got %d", len(rec.Fits))
	}
	for i := 1; i < len(rec.Fits); i++ {
		if rec.Fits[i-1].MinRAMGiB < rec.Fits[i].MinRAMGiB {
			t.Errorf("fits not largest-first at %d: %d before %d", i, rec.Fits[i-1].MinRAMGiB, rec.Fits[i].MinRAMGiB)
		}
	}
}

func TestLookupAliasAndRef(t *testing.T) {
	m, ok := Lookup("qwen-coder")
	if !ok || m.Params != "32B" {
		t.Fatalf("Lookup(alias) = %+v, %v", m, ok)
	}
	if _, ok := Lookup(m.Ref); !ok {
		t.Errorf("Lookup by full ref %q should also resolve", m.Ref)
	}
	if _, ok := Lookup("nope"); ok {
		t.Errorf("Lookup of unknown handle should be !ok")
	}
}

func TestResolveRef(t *testing.T) {
	// A curated alias → its pinned ref, curated=true.
	ref, curated, err := ResolveRef("qwen-coder")
	if err != nil || !curated || ref != "qwen2.5-coder:32b-instruct-q4_K_M" {
		t.Fatalf("ResolveRef(alias) = %q, %v, %v", ref, curated, err)
	}
	// An uncurated but valid ref → passthrough, curated=false.
	ref, curated, err = ResolveRef("phi4:14b")
	if err != nil || curated || ref != "phi4:14b" {
		t.Fatalf("ResolveRef(raw) = %q, %v, %v", ref, curated, err)
	}
	// An injection attempt → rejected.
	if _, _, err := ResolveRef("evil; rm -rf /"); err == nil {
		t.Errorf("ResolveRef should reject shell-y refs")
	}
}

func TestValidateRef(t *testing.T) {
	for _, ok := range []string{"qwen2.5-coder:32b-instruct-q4_K_M", "llama3.3:70b", "org/model:tag"} {
		if err := ValidateRef(ok); err != nil {
			t.Errorf("ValidateRef(%q) unexpected error: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "x;y", "$(whoami)", "a|b"} {
		if err := ValidateRef(bad); err == nil {
			t.Errorf("ValidateRef(%q) should have errored", bad)
		}
	}
}

func TestParseOllamaList(t *testing.T) {
	out := "NAME                             ID              SIZE      MODIFIED\n" +
		"qwen2.5-coder:32b-instruct-q4_K_M  abc123        20 GB     2 days ago\n" +
		"phi4:14b                            def456        9 GB      1 week ago\n"
	models := parseOllamaList(out)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(models), models)
	}
	if models[0].Ref != "qwen2.5-coder:32b-instruct-q4_K_M" || models[0].Size != "20 GB" {
		t.Errorf("row0 = %+v", models[0])
	}
	if models[0].Alias != "qwen-coder" {
		t.Errorf("curated ref should carry its alias, got %q", models[0].Alias)
	}
	if models[1].Alias != "" {
		t.Errorf("uncurated ref should have no alias, got %q", models[1].Alias)
	}
}
