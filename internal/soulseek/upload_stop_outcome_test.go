package soulseek

import "testing"

func TestStopUploadsReportsCancelledNotCompleted(t *testing.T) {
	c := NewClient(ClientConfig{Uploads: NewUploadManager(1)})
	target := UploadTarget{Username: "peer", Filename: "file", Attempt: 1}
	for _, state := range []string{"completed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			done := make(chan struct{})
			close(done)
			a := &uploadAttempt{target: target, state: state, done: done, cancel: func() {}}
			c.uploads[downloadKey(target.Username, target.Filename)] = a
			got := c.StopUploads([]UploadTarget{target, target, {Username: "peer", Filename: "file", Attempt: 2}}, true)
			if state == "completed" && len(got) != 0 {
				t.Fatalf("completion counted as cancellation: %+v", got)
			}
			if state == "cancelled" && (len(got) != 1 || got[0] != target) {
				t.Fatalf("wrong cancelled identities: %+v", got)
			}
		})
	}
}
