package server

import (
	"reflect"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestItemEventDataCarriesStatelessConsumerCorrelationFields(t *testing.T) {
	t.Parallel()
	item := store.Item{
		ID: "item-123", WorkspaceID: 42,
		Metadata:            map[string]any{"repository": "islandora", "collection": "newspapers"},
		ExternalReferenceID: "islandora:abc", CallerIdempotencyKey: "caller-request-9",
	}
	image := store.ItemImage{ID: 99, ItemID: item.ID, CanvasURI: "https://repository.example/canvas/99"}
	got := itemEventData(item, image, 7)
	want := map[string]any{
		"workspaceId": uint64(42), "itemId": "item-123", "itemImageId": uint64(99),
		"canvasUri": "https://repository.example/canvas/99", "revision": uint64(7),
		"metadata": item.Metadata, "externalReferenceId": "islandora:abc", "idempotencyKey": "caller-request-9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("item event data = %#v, want %#v", got, want)
	}

	minimal := itemEventData(store.Item{ID: item.ID, WorkspaceID: item.WorkspaceID}, image, 1)
	if _, ok := minimal["externalReferenceId"]; ok {
		t.Fatal("empty external reference was emitted")
	}
	if _, ok := minimal["idempotencyKey"]; ok {
		t.Fatal("empty caller idempotency key was emitted")
	}
}
