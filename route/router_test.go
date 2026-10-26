package route

import "testing"

func TestSnapshotMatch(t *testing.T) {
	snapshot := Default()

	tests := []struct {
		path string
		id   string
		ok   bool
	}{
		{path: "/api/v1/user", id: "user", ok: true},
		{path: "/api/v1/user/auth/login", id: "user", ok: true},
		{path: "/api/v1/userx", ok: false},
		{path: "/api/v1/chat/groups", id: "chat", ok: true},
		{path: "/ws/chat", id: "chat-ws", ok: true},
		{path: "/ws/chat/extra", ok: false},
		{path: "/ws/game", id: "game-ws", ok: true},
		{path: "/ws/game/extra", ok: false},
		{path: "/api/v1/game/tournaments", id: "game", ok: true},
		{path: "/unknown", ok: false},
	}
	for _, tt := range tests {
		got, ok := snapshot.Match(tt.path)
		if ok != tt.ok {
			t.Fatalf("Match(%q) ok = %v, want %v", tt.path, ok, tt.ok)
		}
		if ok && got.ID != tt.id {
			t.Fatalf("Match(%q) = %s, want %s", tt.path, got.ID, tt.id)
		}
	}
}

func TestSnapshotMatchUsesLongestPrefix(t *testing.T) {
	snapshot, err := Parse([]byte(`
version: 1
gateway:
  routes:
    - id: short
      prefix: /api
      service: tf-user
    - id: long
      prefix: /api/v1/chat
      service: tf-chat
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got, ok := snapshot.Match("/api/v1/chat/groups")
	if !ok || got.ID != "long" {
		t.Fatalf("Match() = %+v, %v; want long", got, ok)
	}
	got, ok = snapshot.Match("/api/v1/reader")
	if !ok || got.ID != "short" {
		t.Fatalf("Match() = %+v, %v; want short", got, ok)
	}
}
