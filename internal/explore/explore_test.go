package explore

import (
	"strings"
	"testing"
)

func TestRecommendResourceConfigUsesCondaBase(t *testing.T) {
	d := &Discovery{
		CondaBase: "/opt/conda",
		CondaEnvs: []CondaEnv{
			{Name: "base", Path: "/opt/conda"},
			{Name: "llm", Path: "/opt/conda/envs/llm"},
		},
	}

	rec := RecommendResourceConfig(d)
	if rec == nil {
		t.Fatalf("expected recommendation")
	}
	if rec.RemotePath != "/opt/conda/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Fatalf("remote path = %q", rec.RemotePath)
	}
	if rec.CondaInit != "/opt/conda/etc/profile.d/conda.sh" {
		t.Fatalf("conda init = %q", rec.CondaInit)
	}
	if rec.CondaEnv != "base" {
		t.Fatalf("conda env = %q", rec.CondaEnv)
	}
	for _, want := range []string{"--remote-path", "--conda-base", "--conda-init", "--conda-env 'base'"} {
		if !strings.Contains(rec.AddFlags, want) {
			t.Fatalf("flags missing %q: %s", want, rec.AddFlags)
		}
	}
}

func TestFormatDiscoveryShowsRecommendedConfig(t *testing.T) {
	text := FormatDiscovery(&Discovery{
		Host: "gpu-box",
		OS:   "Linux",
		RecommendedConfig: &ResourceRecommendation{
			RemotePath: "/opt/conda/bin:/usr/bin:/bin",
			CondaBase:  "/opt/conda",
			CondaInit:  "/opt/conda/etc/profile.d/conda.sh",
			CondaEnv:   "base",
			AddFlags:   "--remote-path '/opt/conda/bin:/usr/bin:/bin' --conda-env 'base'",
		},
	})
	for _, want := range []string{"Recommended resource config:", "remote_path:", "add/update flags:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted discovery missing %q:\n%s", want, text)
		}
	}
}
