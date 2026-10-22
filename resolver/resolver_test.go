package resolver

import (
	"net/url"
	"testing"
)

type fakeDiscovery struct {
	instances map[string][]Instance
	seen      map[string]bool
}

func (f *fakeDiscovery) Instances(service string) ([]Instance, bool) {
	return f.instances[service], f.seen[service]
}

func TestResolveRoundRobin(t *testing.T) {
	discovery := &fakeDiscovery{
		instances: map[string][]Instance{
			"tf-user": {
				{IP: "10.0.0.1", Port: 8101, Metadata: map[string]string{"version": "v1"}},
				{IP: "10.0.0.2", Port: 8101},
			},
		},
		seen: map[string]bool{"tf-user": true},
	}
	r := New(discovery, nil)
	first, err := r.Resolve("tf-user")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	second, err := r.Resolve("tf-user")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if first.Instance == second.Instance {
		t.Fatalf("expected round robin, got %s twice", first.Instance)
	}
	if first.Version != "v1" {
		t.Fatalf("version = %q, want v1", first.Version)
	}
}

func TestResolveFallsBackToStatic(t *testing.T) {
	target, _ := url.Parse("http://user:8101")
	r := New(&fakeDiscovery{seen: map[string]bool{}}, map[string]*url.URL{"tf-user": target})
	result, err := r.Resolve("tf-user")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !result.Fallback || result.Instance != "user:8101" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveNoInstance(t *testing.T) {
	r := New(&fakeDiscovery{seen: map[string]bool{}}, nil)
	if _, err := r.Resolve("tf-user"); err != ErrNoInstance {
		t.Fatalf("Resolve() error = %v, want ErrNoInstance", err)
	}
}

func TestResolveFallbackDisabled(t *testing.T) {
	target, _ := url.Parse("http://user:8101")
	r := New(&fakeDiscovery{seen: map[string]bool{}}, map[string]*url.URL{"tf-user": target})
	r.SetFallbackEnabled(false)
	if _, err := r.Resolve("tf-user"); err != ErrNoInstance {
		t.Fatalf("Resolve() error = %v, want ErrNoInstance", err)
	}
}

func TestStaticTargetsSkipsInvalid(t *testing.T) {
	targets := StaticTargets(map[string]string{
		"tf-user":   "http://user:8101",
		"tf-chat":   "",
		"tf-reader": "not-a-url",
	})
	if len(targets) != 1 || targets["tf-user"] == nil {
		t.Fatalf("targets = %+v", targets)
	}
}
