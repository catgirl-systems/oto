package soulseek

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

func TestUploadPreferredClassAndProjectedPositions(t *testing.T) {
	for _, schedule := range []string{UploadScheduleFIFO, UploadScheduleRoundRobin, UploadScheduleRandom, UploadScheduleSmallestFirst} {
		t.Run(schedule, func(t *testing.T) {
			m := NewUploadManager(1)
			m.Configure(UploadPolicy{Scheduling: schedule})
			blocker := m.Enqueue("active", TransferRequest{})
			normal := m.Enqueue("normal", TransferRequest{Filename: "normal", Size: 1})
			first := m.Enqueue("buddy", TransferRequest{Filename: "buddy", Size: 100})
			second := m.Enqueue("supporter", TransferRequest{Filename: "supporter", Size: 50})
			users := map[string]UploadUserPolicy{"buddy": {Preferred: true}, "supporter": {Preferred: true}}
			m.SetUserPolicies(users)
			delete(users, "buddy")
			if !uploadReady(blocker) || uploadReady(first) || uploadReady(second) {
				t.Fatal("priority preempted active transfer")
			}
			m.random = rand.NewPCG(1, 2)
			before, _ := m.random.MarshalBinary()
			jobs := []*UploadJob{normal, first, second}
			positions := map[*UploadJob]uint32{}
			for _, job := range jobs {
				p, err := m.Position(context.Background(), job)
				if err != nil {
					t.Fatal(err)
				}
				positions[job] = p
			}
			after, _ := m.random.MarshalBinary()
			if !bytes.Equal(before, after) {
				t.Fatal("position query consumed scheduler randomness")
			}
			if positions[normal] != 3 || positions[first] >= 3 || positions[second] >= 3 || positions[first] == positions[second] {
				t.Fatal("preferred class positions", positions)
			}
			sort.Slice(jobs, func(i, j int) bool { return positions[jobs[i]] < positions[jobs[j]] })
			m.Done(blocker)
			for _, job := range jobs {
				if !uploadReady(job) {
					t.Fatal("position diverged from scheduling", job.User, positions)
				}
				m.Done(job)
			}
		})
	}
}

func TestUploadUserPolicyChangesAndLimitExemption(t *testing.T) {
	m := NewUploadManager(1)
	m.Configure(UploadPolicy{MaxQueuedFilesPerUser: 1, MaxQueuedBytesPerUser: 1})
	m.SetUserPolicies(map[string]UploadUserPolicy{"buddy": {Preferred: true, ExemptLimits: true}})
	one, err := m.TryEnqueue("buddy", TransferRequest{Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.TryEnqueue("buddy", TransferRequest{Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.TryEnqueue("normal", TransferRequest{Size: 2}); !errors.Is(err, ErrTooManyUploadBytes) {
		t.Fatal("nonbuddy exempted", err)
	}
	if m.outstandingFiles["buddy"] != 2 || m.outstandingBytes["buddy"] != 4 || uploadReady(two) {
		t.Fatal("exemption bypassed accounting or per-user slot")
	}
	m.SetUserPolicies(nil)
	if !uploadReady(one) {
		t.Fatal("flag change preempted accepted upload")
	}
	if _, err := m.TryEnqueue("buddy", TransferRequest{}); !errors.Is(err, ErrTooManyUploadFiles) {
		t.Fatal("removed exemption retained", err)
	}
	restored, err := m.TryEnqueueRestored("buddy", TransferRequest{Size: 1})
	if err != nil {
		t.Fatal("accepted recovery reapplied caps", err)
	}
	for _, job := range []*UploadJob{one, two, restored} {
		m.Done(job)
	}
	if m.outstandingFiles["buddy"] != 0 || m.outstandingBytes["buddy"] != 0 {
		t.Fatal("exempt accounting retained")
	}
	for _, restore := range []bool{false, true} {
		m.SetUserPolicies(map[string]UploadUserPolicy{"buddy": {ExemptLimits: true}})
		enqueue := m.TryEnqueue
		if restore {
			enqueue = m.TryEnqueueRestored
		}
		m.outstandingBytes["buddy"] = ^uint64(0)
		if job, err := enqueue("buddy", TransferRequest{Size: 1}); job != nil || !errors.Is(err, ErrTooManyUploadBytes) {
			t.Fatal("byte overflow admitted", job, err)
		}
		m.outstandingBytes["buddy"] = 0
		m.outstandingFiles["buddy"] = ^uint64(0)
		if job, err := enqueue("buddy", TransferRequest{}); job != nil || !errors.Is(err, ErrTooManyUploadFiles) {
			t.Fatal("file overflow admitted", job, err)
		}
		m.outstandingFiles["buddy"] = 0
	}
}

func TestUploadPositionCancellationAndBusyUser(t *testing.T) {
	m := NewUploadManager(1)
	active := m.Enqueue("busy", TransferRequest{})
	busy := m.Enqueue("busy", TransferRequest{Filename: "next"})
	other := m.Enqueue("other", TransferRequest{})
	for job, want := range map[*UploadJob]uint32{active: 0, busy: 1, other: 2} {
		p, err := m.Position(context.Background(), job)
		if err != nil || p != want {
			t.Fatal(p, want, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Position(ctx, busy); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled projection", err)
	}
	m.positionSlot <- struct{}{}
	if _, err := m.Position(ctx, busy); !errors.Is(err, context.Canceled) {
		t.Fatal("blocked projection ignored cancellation", err)
	}
	<-m.positionSlot
	m.CancelJobs([]*UploadJob{busy})
	if _, err := m.Position(context.Background(), busy); !errors.Is(err, ErrTransferCancelled) {
		t.Fatal("cancelled job position", err)
	}
	m.Done(busy)
	m.Done(active)
	m.Done(other)
}

func TestUploadRecoverySelectsPriorityAfterWholeQueueRestored(t *testing.T) {
	m := NewUploadManager(1)
	ready := make(chan struct{})
	client := NewClient(ClientConfig{Uploads: m, UploadsReady: ready})
	defer client.Close()
	m.SetUserPolicies(map[string]UploadUserPolicy{"buddy": {Preferred: true}})
	normal := m.EnqueueRestored("normal", TransferRequest{})
	buddy := m.EnqueueRestored("buddy", TransferRequest{})
	if uploadReady(normal) || uploadReady(buddy) {
		t.Fatal("partial recovery reserved slots")
	}
	close(ready)
	select {
	case <-buddy.Ready:
	case <-time.After(time.Second):
		t.Fatal("restored priority not selected")
	}
	if uploadReady(normal) {
		t.Fatal("recovery preempted preferred class")
	}
	m.Done(buddy)
	if !uploadReady(normal) {
		t.Fatal("normal restoration not resumed")
	}
	m.Done(normal)
}

func TestUploadProjectedPositionsRespectPromotionBatchSlots(t *testing.T) {
	m := NewUploadManager(2)
	ready := make(chan struct{})
	client := NewClient(ClientConfig{Uploads: m, UploadsReady: ready})
	defer client.Close()
	a1 := m.EnqueueRestored("A", TransferRequest{Filename: "first"})
	a2 := m.EnqueueRestored("A", TransferRequest{Filename: "second"})
	b1 := m.EnqueueRestored("B", TransferRequest{})
	for job, want := range map[*UploadJob]uint32{a1: 1, b1: 2, a2: 3} {
		position, err := m.Position(context.Background(), job)
		if err != nil || position != want {
			t.Fatal(job.User, position, want, err)
		}
	}
	close(ready)
	select {
	case <-b1.Ready:
	case <-time.After(time.Second):
		t.Fatal("second slot not assigned to B")
	}
	if !uploadReady(a1) || uploadReady(a2) {
		t.Fatal("projection disagreed with initial promotion batch")
	}
	m.Done(a1)
	m.Done(b1)
	m.Done(a2)
}
