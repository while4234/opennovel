package host

import (
	"encoding/json"
	"testing"
	"time"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestHostEventPersistenceIncludesStartAndSystemEvents(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	h := &Host{
		store:  st,
		events: make(chan Event, 8),
	}
	started := time.Now().UTC()
	h.emitEvent(Event{ID: "call-1", Time: started, Category: "TOOL", Agent: "writer", Summary: "draft_chapter", Level: "info"})
	h.emitEvent(Event{ID: "call-1", Time: started, FinishedAt: started.Add(time.Second), Category: "TOOL", Agent: "writer", Summary: "draft_chapter", Level: "success"})
	h.emitEvent(Event{Category: "SYSTEM", Summary: "Coordinator 停止", Level: "warn"})

	items, err := st.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("persisted event count = %d, want 3", len(items))
	}
	var start Event
	if data, marshalErr := json.Marshal(items[0].Payload); marshalErr != nil || json.Unmarshal(data, &start) != nil || start.ID != "call-1" || !start.Running() {
		t.Fatalf("start event payload = %#v, want running call", items[0].Payload)
	}
	var finish Event
	if data, marshalErr := json.Marshal(items[1].Payload); marshalErr != nil || json.Unmarshal(data, &finish) != nil || finish.ID != "call-1" || finish.Running() {
		t.Fatalf("finish event payload = %#v, want completed call", items[1].Payload)
	}
	if items[2].Category != "SYSTEM" || items[2].Summary != "Coordinator 停止" {
		t.Fatalf("system event = %+v", items[2])
	}
}
