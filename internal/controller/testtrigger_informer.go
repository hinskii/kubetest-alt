/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// informerEvent is the shape delivered from the informer manager to the
// TestTrigger controller's event loop.
type informerEvent struct {
	// Kind is one of "created" | "modified" | "deleted" — matches the CRD
	// enum on TestTriggerSpec.Event.
	Kind string
	GVK  schema.GroupVersionKind
	Obj  *unstructured.Unstructured
}

// TestTriggerSpec.Event enum mirrors (kept as consts to avoid stringly
// typed comparisons drifting between files).
const (
	TriggerEventCreated  = "created"
	TriggerEventModified = "modified"
	TriggerEventDeleted  = "deleted"
)

// informerManager owns dynamic informers PER UNIQUE GVK — not per trigger.
// Ref-counted registration means:
//   - 50 TestTriggers on Deployments ⇒ 1 informer on apps/v1 Deployment,
//     not 50 (avoids apiserver watch-quota exhaustion at scale)
//   - deleting the last TestTrigger of a GVK ⇒ informer stops (no watch
//     leaks; a Register of a NEW GVK starts a fresh informer for that GVK
//     and nothing else)
//
// Lifecycle:
//   - construct with newInformerManager
//   - Start(ctx) once (binds parent context; per-GVK informer contexts
//     derive from it)
//   - Register / Unregister freely from the reconciler's Reconcile method
//   - Stop() on manager exit cancels every informer
type informerManager struct {
	mu sync.Mutex

	dyn    dynamic.Interface
	mapper meta.RESTMapper

	// regs is (triggerKey → registered GVK). Present so Unregister can
	// decrement the correct GVK's ref count without re-parsing spec.resource
	// (the trigger might already be Deleted from the store).
	regs map[types.NamespacedName]schema.GroupVersionKind

	// gvkRuns holds per-GVK informer lifetime state.
	gvkRuns map[schema.GroupVersionKind]*informerRun

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// out is the delivery channel — one buffered channel for all GVKs. The
	// consumer (TestTriggerController event loop) selects on it. We NEVER
	// close(out) on Stop: informer callbacks race with close and would
	// panic. Instead we rely on ctx cancellation to let the consumer exit.
	out chan informerEvent
}

type informerRun struct {
	refs int
	stop context.CancelFunc
}

// outBufferSize is the events channel buffer. Sized so a burst of pod/deploy
// churn on one GVK doesn't drop events under a slow consumer. Deliveries
// select on ctx.Done() too, so on Stop the sender exits promptly.
const outBufferSize = 256

func newInformerManager(dyn dynamic.Interface, mapper meta.RESTMapper) *informerManager {
	return &informerManager{
		dyn:     dyn,
		mapper:  mapper,
		regs:    map[types.NamespacedName]schema.GroupVersionKind{},
		gvkRuns: map[schema.GroupVersionKind]*informerRun{},
		out:     make(chan informerEvent, outBufferSize),
	}
}

// Events returns the delivery channel. The channel is NOT closed by Stop —
// consumers must exit on their own ctx.Done() signal.
func (im *informerManager) Events() <-chan informerEvent { return im.out }

// Start binds the parent context. Must be called before the first Register.
// Idempotent for the current context — a second Start replaces the first
// (used only during test setup).
func (im *informerManager) Start(ctx context.Context) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.rootCancel != nil {
		im.rootCancel()
	}
	im.rootCtx, im.rootCancel = context.WithCancel(ctx)
}

// Stop cancels every per-GVK informer. Safe to call multiple times.
func (im *informerManager) Stop() {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.rootCancel != nil {
		im.rootCancel()
		im.rootCancel = nil
	}
	for gvk, run := range im.gvkRuns {
		if run.stop != nil {
			run.stop()
		}
		delete(im.gvkRuns, gvk)
	}
	im.regs = map[types.NamespacedName]schema.GroupVersionKind{}
}

// Register attaches a trigger to a GVK. Behavior:
//   - first registration for a GVK starts the informer for that GVK
//   - a subsequent registration for the SAME GVK is a no-op (ref count only)
//   - a registration with a DIFFERENT GVK first releases the old
//     registration (which may stop the old GVK's informer if this was the
//     last consumer)
func (im *informerManager) Register(triggerKey types.NamespacedName, gvk schema.GroupVersionKind) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	if prev, ok := im.regs[triggerKey]; ok {
		if prev == gvk {
			return nil
		}
		im.releaseLocked(prev)
		delete(im.regs, triggerKey)
	}

	if _, exists := im.gvkRuns[gvk]; !exists {
		if err := im.startInformerLocked(gvk); err != nil {
			return err
		}
	}
	im.gvkRuns[gvk].refs++
	im.regs[triggerKey] = gvk
	return nil
}

// Unregister detaches a trigger; stops the GVK's informer if this was the
// last consumer of that GVK. No-op if the trigger wasn't registered.
func (im *informerManager) Unregister(triggerKey types.NamespacedName) {
	im.mu.Lock()
	defer im.mu.Unlock()
	gvk, ok := im.regs[triggerKey]
	if !ok {
		return
	}
	delete(im.regs, triggerKey)
	im.releaseLocked(gvk)
}

// gvkRefs snapshots the current per-GVK ref counts. Unexported: used by
// tests inside this package to prove ref-counting works.
func (im *informerManager) gvkRefs() map[schema.GroupVersionKind]int {
	im.mu.Lock()
	defer im.mu.Unlock()
	out := make(map[schema.GroupVersionKind]int, len(im.gvkRuns))
	for gvk, r := range im.gvkRuns {
		out[gvk] = r.refs
	}
	return out
}

func (im *informerManager) releaseLocked(gvk schema.GroupVersionKind) {
	run, ok := im.gvkRuns[gvk]
	if !ok {
		return
	}
	run.refs--
	if run.refs <= 0 {
		if run.stop != nil {
			run.stop()
		}
		delete(im.gvkRuns, gvk)
	}
}

func (im *informerManager) startInformerLocked(gvk schema.GroupVersionKind) error {
	if im.rootCtx == nil {
		return fmt.Errorf("informerManager: Start not called before Register")
	}
	mapping, err := im.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("resolve GVR for %s: %w", gvk, err)
	}
	// Per-GVK factory so a single GVK can be started/stopped cleanly
	// without touching any other GVK's informer. Zero resync period → no
	// periodic re-list; we care about deltas only.
	factory := dynamicinformer.NewDynamicSharedInformerFactory(im.dyn, 0)
	inf := factory.ForResource(mapping.Resource).Informer()

	ctx, cancel := context.WithCancel(im.rootCtx)
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { im.deliver(ctx, gvk, TriggerEventCreated, o) },
		UpdateFunc: func(_, o any) { im.deliver(ctx, gvk, TriggerEventModified, o) },
		DeleteFunc: func(o any) { im.deliver(ctx, gvk, TriggerEventDeleted, o) },
	}); err != nil {
		cancel()
		return fmt.Errorf("register informer handlers: %w", err)
	}
	go inf.Run(ctx.Done())
	im.gvkRuns[gvk] = &informerRun{stop: cancel}
	return nil
}

// deliver casts the informer callback payload into an *Unstructured and
// pushes it onto the events channel. Handles the DeletedFinalStateUnknown
// wrapper the informer may deliver when it missed a delete during relist.
func (im *informerManager) deliver(ctx context.Context, gvk schema.GroupVersionKind, kind string, o any) {
	obj, ok := o.(*unstructured.Unstructured)
	if !ok {
		// DeletedFinalStateUnknown arrives when the informer missed a
		// delete during a relist; unwrap and deliver.
		if tomb, tombOk := o.(cache.DeletedFinalStateUnknown); tombOk {
			if inner, innerOk := tomb.Obj.(*unstructured.Unstructured); innerOk {
				obj = inner
			}
		}
	}
	if obj == nil {
		log.FromContext(ctx).V(1).Info("informer: unexpected object type",
			"gvk", gvk.String(), "kind", kind, "gotType", fmt.Sprintf("%T", o))
		return
	}
	ev := informerEvent{Kind: kind, GVK: gvk, Obj: obj.DeepCopy()}
	select {
	case im.out <- ev:
	case <-ctx.Done():
	}
}
