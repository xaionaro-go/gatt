package jni

import (
	"testing"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
)

func newTestCentral(
	t *testing.T,
	gs *gattServerState,
	addr string,
	closeCount *int,
) *central {
	t.Helper()

	c := newCentral(nil, &bluetooth.Device{Obj: &jnipkg.Object{}}, addr)
	c.deleteGlobalRef = func(*jnipkg.Object) error {
		if !gs.mu.TryLock() {
			t.Fatalf("central closed while gattServerState mutex was held")
		}
		gs.mu.Unlock()
		(*closeCount)++
		return nil
	}
	return c
}

func TestGattServerStateConnectedCentralClosesDuplicateOutsideLock(t *testing.T) {
	gs := &gattServerState{centrals: make(map[string]*central)}
	oldCloseCount := 0
	newCloseCount := 0
	addr := "04:A8:5A:58:60:95"
	oldCentral := newTestCentral(t, gs, addr, &oldCloseCount)
	replacementCentral := newTestCentral(t, gs, addr, &newCloseCount)
	gs.centrals[addr] = oldCentral

	if err := gs.rememberConnectedCentral(replacementCentral); err != nil {
		t.Fatalf("rememberConnectedCentral returned error: %v", err)
	}

	if oldCloseCount != 1 {
		t.Fatalf("old central close count = %d, want 1", oldCloseCount)
	}
	if newCloseCount != 0 {
		t.Fatalf("new central close count = %d, want 0", newCloseCount)
	}
	if got := gs.centrals[addr]; got != replacementCentral {
		t.Fatalf("stored central = %p, want %p", got, replacementCentral)
	}
}

func TestGattServerStateConnectedCentralRemovesStaleNotifiersForDuplicateAddress(t *testing.T) {
	addr := "04:A8:5A:58:60:95"
	oldCloseCount := 0
	newCloseCount := 0
	gs := &gattServerState{
		centrals:  make(map[string]*central),
		notifiers: make(map[string]*serverNotifier),
	}
	oldCentral := newTestCentral(t, gs, addr, &oldCloseCount)
	replacementCentral := newTestCentral(t, gs, addr, &newCloseCount)
	gs.centrals[addr] = oldCentral

	notifierByCentral := &serverNotifier{c: oldCentral}
	notifierByKeySuffix := &serverNotifier{c: &central{addr: "other"}}
	unrelatedNotifier := &serverNotifier{c: &central{addr: "other"}}
	gs.notifiers[notifierKey("primary", addr)] = notifierByCentral
	gs.notifiers["legacy:"+addr] = notifierByKeySuffix
	gs.notifiers[notifierKey("primary", "other")] = unrelatedNotifier

	if err := gs.rememberConnectedCentral(replacementCentral); err != nil {
		t.Fatalf("rememberConnectedCentral returned error: %v", err)
	}

	if oldCloseCount != 1 {
		t.Fatalf("old central close count = %d, want 1", oldCloseCount)
	}
	if newCloseCount != 0 {
		t.Fatalf("new central close count = %d, want 0", newCloseCount)
	}
	if got := gs.centrals[addr]; got != replacementCentral {
		t.Fatalf("stored central = %p, want %p", got, replacementCentral)
	}
	if _, ok := gs.notifiers[notifierKey("primary", addr)]; ok {
		t.Fatalf("duplicate replacement left notifier matched by central address")
	}
	if _, ok := gs.notifiers["legacy:"+addr]; ok {
		t.Fatalf("duplicate replacement left notifier matched by key suffix")
	}
	if !notifierByCentral.Done() {
		t.Fatalf("duplicate replacement did not stop notifier matched by central address")
	}
	if !notifierByKeySuffix.Done() {
		t.Fatalf("duplicate replacement did not stop notifier matched by key suffix")
	}
	if _, ok := gs.notifiers[notifierKey("primary", "other")]; !ok {
		t.Fatalf("duplicate replacement removed unrelated notifier")
	}
	if unrelatedNotifier.Done() {
		t.Fatalf("duplicate replacement stopped unrelated notifier")
	}
}

func TestGattServerStateConnectedCentralNoopForSameCentral(t *testing.T) {
	addr := "04:A8:5A:58:60:95"
	closeCount := 0
	gs := &gattServerState{
		centrals:  make(map[string]*central),
		notifiers: make(map[string]*serverNotifier),
	}
	c := newTestCentral(t, gs, addr, &closeCount)
	gs.centrals[addr] = c
	activeNotifier := &serverNotifier{c: c}
	gs.notifiers[notifierKey("primary", addr)] = activeNotifier

	if err := gs.rememberConnectedCentral(c); err != nil {
		t.Fatalf("rememberConnectedCentral returned error: %v", err)
	}

	if closeCount != 0 {
		t.Fatalf("central close count = %d, want 0", closeCount)
	}
	if got := gs.centrals[addr]; got != c {
		t.Fatalf("stored central = %p, want %p", got, c)
	}
	if got := gs.notifiers[notifierKey("primary", addr)]; got != activeNotifier {
		t.Fatalf("same-central reconnect changed notifier = %p, want %p", got, activeNotifier)
	}
	if activeNotifier.Done() {
		t.Fatalf("same-central reconnect stopped existing notifier")
	}
}

func TestGattServerStateDisconnectCentralClosesRemovedCentralAndStopsNotifiers(t *testing.T) {
	addr := "04:A8:5A:58:60:95"
	closeCount := 0
	gs := &gattServerState{
		centrals:  make(map[string]*central),
		notifiers: make(map[string]*serverNotifier),
	}
	c := newTestCentral(t, gs, addr, &closeCount)
	gs.centrals[addr] = c
	activeNotifier := &serverNotifier{c: c}
	otherNotifier := &serverNotifier{c: &central{addr: "other"}}
	gs.notifiers["active"] = activeNotifier
	gs.notifiers["other"] = otherNotifier

	removed, exists, err := gs.disconnectCentral(addr)
	if err != nil {
		t.Fatalf("disconnectCentral returned error: %v", err)
	}
	if !exists {
		t.Fatalf("disconnectCentral exists = false, want true")
	}
	if removed != c {
		t.Fatalf("disconnectCentral removed = %p, want %p", removed, c)
	}
	if closeCount != 1 {
		t.Fatalf("central close count = %d, want 1", closeCount)
	}
	if _, ok := gs.centrals[addr]; ok {
		t.Fatalf("disconnectCentral left central stored for %s", addr)
	}
	if _, ok := gs.notifiers["active"]; ok {
		t.Fatalf("disconnectCentral left notifier for removed central")
	}
	if !activeNotifier.Done() {
		t.Fatalf("disconnectCentral did not stop notifier for removed central")
	}
	if _, ok := gs.notifiers["other"]; !ok {
		t.Fatalf("disconnectCentral removed unrelated notifier")
	}
	if otherNotifier.Done() {
		t.Fatalf("disconnectCentral stopped unrelated notifier")
	}
}

func TestGattServerStateCloseCentralsClosesAllRemainingCentralsOutsideLock(t *testing.T) {
	gs := &gattServerState{centrals: make(map[string]*central)}
	firstCloseCount := 0
	secondCloseCount := 0
	gs.centrals["first"] = newTestCentral(t, gs, "first", &firstCloseCount)
	gs.centrals["second"] = newTestCentral(t, gs, "second", &secondCloseCount)

	if err := gs.closeCentrals(); err != nil {
		t.Fatalf("closeCentrals returned error: %v", err)
	}

	if firstCloseCount != 1 {
		t.Fatalf("first central close count = %d, want 1", firstCloseCount)
	}
	if secondCloseCount != 1 {
		t.Fatalf("second central close count = %d, want 1", secondCloseCount)
	}
	if len(gs.centrals) != 0 {
		t.Fatalf("closeCentrals left %d centrals stored, want 0", len(gs.centrals))
	}
}
