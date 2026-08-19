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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// ClientTemplateStore is the production TemplateStore backed by the
// controller-runtime client (goes through the manager cache, so lookups
// are O(1) after initial sync).
type ClientTemplateStore struct {
	Client client.Client
	// Ctx is the context used for Get. Set to the manager's root context
	// so lookups get cancelled on shutdown; otherwise defaults to
	// context.Background at call time.
	Ctx context.Context
}

// Get implements resolver.TemplateStore. Returns resolver.ErrTemplateNotFound
// on IsNotFound so callers can errors.Is against the sentinel.
func (s *ClientTemplateStore) Get(namespace, name string) (*testsv1alpha1.TestTemplate, error) {
	ctx := s.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var tmpl testsv1alpha1.TestTemplate
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, resolver.ErrTemplateNotFound
		}
		return nil, err
	}
	return &tmpl, nil
}
