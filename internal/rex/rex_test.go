package rex

import "testing"

func TestParseEvent(t *testing.T) {
	line := `{"type":"session_event","payload":{"session_id":"session:1","event":{"type":"block_event","payload":{"block_id":"block:2","creator_name":"com.superlogical.terminal","name":"program_status_changed","payload":{"id":"","record":{"state":"blocked","kind":"question","msg":"Commit?","updated_at":"2026-11-10T09:01:39.791569+11:00"},"reason":"report"}}}}}`
	ev, ok := ParseEvent([]byte(line))
	if !ok || ev.Type != "block_event" || ev.SessionID != "session:1" || ev.BlockID != "block:2" ||
		ev.Name != "program_status_changed" || ev.Record == nil || ev.Record.State != "blocked" || ev.Record.Kind != "question" {
		t.Fatalf("got %+v", ev)
	}
	ev, ok = ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:1","event":{"type":"window_label_changed","payload":{}}}}`))
	if !ok || ev.Type != "window_label_changed" {
		t.Fatalf("got %+v", ev)
	}
	ev, ok = ParseEvent([]byte(`{"type":"client_joined","payload":{}}`))
	if !ok || ev.Type != "client_joined" {
		t.Fatalf("got %+v", ev)
	}
	if _, ok := ParseEvent([]byte("Error: interrupted")); ok {
		t.Error("parsed a non-JSON line")
	}
}

func TestNotFound(t *testing.T) {
	if !NotFound(&CallError{Msg: "block not found (status=404 code=not_found)"}) {
		t.Error("not_found should count")
	}
	if NotFound(&CallError{Msg: "permission denied"}) {
		t.Error("other failures should not")
	}
}
