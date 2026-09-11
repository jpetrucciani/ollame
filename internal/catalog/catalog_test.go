package catalog

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
)

func baseConfig() config.Config {
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = "http://localhost/v1"
	return cfg
}
func TestExposedBoundary(t *testing.T) {
	cfg := baseConfig()
	cfg.Models.Exclude = []string{"excluded"}
	cfg.Models.Aliases = []config.Alias{{Name: "coder", Target: "provider/private", HideTarget: true}, {Name: "leak", Target: "excluded"}, {Name: "missing", Target: "not-discovered"}}
	catalog, warnings, err := Build(cfg, []Model{{ID: "provider/private"}, {ID: "public"}, {ID: "excluded"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 {
		t.Fatalf("want two absent/filtered alias warnings, got %v", warnings)
	}
	for _, name := range []string{"excluded", "excluded:latest", "leak", "provider/private", "provider/private:latest", "missing"} {
		if _, err := catalog.Resolve(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolved forbidden name %q", name)
		}
	}
	for _, name := range []string{"coder", "CoDeR:LaTeSt", "public", "public:latest"} {
		entry, err := catalog.Resolve(name)
		if err != nil || !catalog.AllowsTarget(entry.Target) {
			t.Errorf("failed eligible resolution %s: %v", name, err)
		}
	}
	if catalog.AllowsTarget("excluded") {
		t.Fatal("excluded target leaked")
	}
	if !catalog.AllowsTarget("provider/private") {
		t.Fatal("alias target absent from send boundary")
	}
}
func TestDerivationCollisionCannotExposeLoser(t *testing.T) {
	cfg := baseConfig()
	catalog, _, err := Build(cfg, []Model{{ID: "a:b"}, {ID: "a-b"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 || catalog.AllowsTarget("a:b") {
		t.Fatal("derivation collision exposed loser")
	}
	if _, err := catalog.Resolve("a:b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("raw ID bypassed collision")
	}
	cfg.Models.Aliases = []config.Alias{{Name: "other", Target: "a:b"}}
	catalog, _, err = Build(cfg, []Model{{ID: "a:b"}, {ID: "a-b"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalog.Resolve("other")
	if err != nil || entry.Target != "a:b" {
		t.Fatal("alias did not restore eligible loser")
	}
	if _, err = catalog.Resolve("a:b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("alias exposed raw losing ID")
	}
}
func TestAliasWinsDerivedNameAndHasNoRawBypass(t *testing.T) {
	cfg := baseConfig()
	cfg.Models.Aliases = []config.Alias{{Name: "a-b", Target: "other"}}
	catalog, _, err := Build(cfg, []Model{{ID: "a:b"}, {ID: "other"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalog.Resolve("a-b")
	if err != nil || entry.Target != "other" {
		t.Fatal("alias did not win")
	}
	if _, err = catalog.Resolve("a:b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("displaced target retained raw access")
	}
}
func TestCapabilityEvidenceAndOverrideOrder(t *testing.T) {
	cfg := baseConfig()
	no := false
	yes := true
	cfg.Models.Overrides = []config.Override{{Match: "*", Behavior: config.Behavior{AddCapabilities: []string{"vision"}}}, {Match: "*", Behavior: config.Behavior{Capabilities: ptr([]string{"completion"})}}, {Match: "*", Behavior: config.Behavior{AddCapabilities: []string{"tools"}}}}
	catalog, _, err := Build(cfg, []Model{{ID: "model", Info: Info{SupportsVision: &yes, SupportsReasoning: &no}}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalog.Resolve("model")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Knowledge["vision"] != No || entry.Knowledge["tools"] != Yes || entry.Knowledge["thinking"] != No {
		t.Fatalf("override order lost: %v", entry.Knowledge)
	}
	cfg.Models.Overrides = nil
	catalog, _, err = Build(cfg, []Model{{ID: "model", Info: Info{SupportsVision: &no}}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err = catalog.Resolve("model")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Knowledge["tools"] != Unknown || !slices.Contains(entry.Capabilities, "tools") || entry.Knowledge["vision"] != No {
		t.Fatal("default advertisement became false evidence")
	}
}
func TestDigestAndImmutability(t *testing.T) {
	cfg := baseConfig()
	cfg.Models.Aliases = []config.Alias{{Name: "alias", Target: "model", Options: map[string]any{"temperature": 0.2}, Behavior: config.Behavior{ExtraBody: map[string]any{"nested": map[string]any{"value": "original"}}}}}
	first, _, err := Build(cfg, []Model{{ID: "model"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := Build(cfg, []Model{{ID: "model"}}, first, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.Resolve("alias")
	b, _ := second.Resolve("alias")
	if a.Digest != b.Digest || !a.ModifiedAt.Equal(b.ModifiedAt) {
		t.Fatal("refresh changed stable metadata")
	}
	a.Options["temperature"] = 99
	a.Knowledge["tools"] = No
	a.Behavior.ExtraBody["nested"].(map[string]any)["value"] = "changed"
	cfg.Models.Aliases[0].Options["temperature"] = 88
	unchanged, _ := first.Resolve("alias")
	if unchanged.Options["temperature"] != 0.2 || unchanged.Knowledge["tools"] != Unknown || unchanged.Behavior.ExtraBody["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("caller mutated immutable catalog")
	}
	third, _, err := Build(cfg, []Model{{ID: "model"}}, first, time.Unix(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := third.Resolve("alias")
	if changed.Digest == b.Digest {
		t.Fatal("effective config did not change digest")
	}
}
func FuzzResolutionStaysInExposedTargets(f *testing.F) {
	for _, s := range []string{"provider/private", "visible", "blocked", "alias", "a:b", "a-b", "MODEL:latest", ""} {
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, requested string, hide bool) {
		cfg := baseConfig()
		cfg.Models.Exclude = []string{"blocked"}
		cfg.Models.Aliases = []config.Alias{{Name: "alias", Target: "provider/private", HideTarget: hide}, {Name: "escape", Target: "blocked"}}
		catalog, _, err := Build(cfg, []Model{{ID: "provider/private"}, {ID: "visible"}, {ID: "blocked"}, {ID: "a:b"}, {ID: "a-b"}}, nil, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		entry, err := catalog.Resolve(requested)
		if err != nil {
			return
		}
		targets := map[string]bool{}
		for _, listed := range catalog.Entries() {
			targets[listed.Target] = true
		}
		if !targets[entry.Target] || entry.Target == "blocked" {
			t.Fatalf("resolution escaped E for %q", requested)
		}
	})
}

func TestCloneDoesNotReapplySupersededCapabilityAdjustments(t *testing.T) {
	cfg := baseConfig()
	cfg.Models.Overrides = []config.Override{{Match: "*", Behavior: config.Behavior{AddCapabilities: []string{"vision"}}}, {Match: "*", Behavior: config.Behavior{Capabilities: ptr([]string{"completion"})}}}
	c, _, err := Build(cfg, []Model{{ID: "m"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := c.Resolve("m")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Knowledge["vision"] != No {
		t.Fatal("cloning revived superseded capability")
	}
}
