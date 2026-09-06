package notifications

import (
	"strings"
	"testing"
)

func TestLifecycleNamesAreStableAndBounded(t *testing.T) {
	longName := strings.Repeat("a", 63)
	secretName := CallbackSecretName(longName)
	if len(secretName) > 63 || secretName != CallbackSecretName(longName) {
		t.Fatalf("invalid callback secret name %q", secretName)
	}
	if secretName == CallbackSecretName(strings.Repeat("a", 62)+"b") {
		t.Fatal("truncated callback secret names collided")
	}
	deliveryName := DeliveryName(strings.Repeat("u", 128))
	if len(deliveryName) > 63 || deliveryName != DeliveryName(strings.Repeat("u", 128)) {
		t.Fatalf("invalid delivery name %q", deliveryName)
	}
	eventID := EventID(strings.Repeat("u", 128))
	if len(eventID) > 128 || eventID != EventID(strings.Repeat("u", 128)) {
		t.Fatalf("invalid event ID %q", eventID)
	}
}
