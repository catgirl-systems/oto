package soulseek

import "testing"

// Bandwidth-driven slots ramp one upload per measured rate and stop once the
// threshold is reached, while the fixed slot count stays an absolute ceiling.
func TestUploadSlotBandwidthAllocation(t *testing.T) {
	m := NewUploadManager(3)
	m.Configure(UploadPolicy{SlotBandwidthBytesPerSecond: 100})
	m.SetBandwidth(100) // Already at the threshold: no admission.
	for _, user := range []string{"a", "b", "c", "d"} {
		if m.Enqueue(user, TransferRequest{Size: 1}) == nil {
			t.Fatal("enqueue failed")
		}
	}
	if _, active := m.DrainStatus(); active != 0 {
		t.Fatalf("threshold ignored, active=%d", active)
	}
	for want := 1; want <= 3; want++ {
		m.SetBandwidth(0)
		if _, active := m.DrainStatus(); active != want {
			t.Fatalf("measurement %d: active=%d, want %d", want, active, want)
		}
	}
	m.SetBandwidth(0)
	if _, active := m.DrainStatus(); active != 3 {
		t.Fatalf("fixed ceiling exceeded, active=%d", active)
	}

	// Fixed mode ignores the measured rate and fills every slot at once.
	fixed := NewUploadManager(2)
	for _, user := range []string{"a", "b", "c"} {
		if fixed.Enqueue(user, TransferRequest{Size: 1}) == nil {
			t.Fatal("enqueue failed")
		}
	}
	if _, active := fixed.DrainStatus(); active != 2 {
		t.Fatalf("fixed slots not filled, active=%d", active)
	}
}
