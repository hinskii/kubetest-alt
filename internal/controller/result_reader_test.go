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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestNoResultReader_AlwaysNotFound(t *testing.T) {
	r, err := NoResultReader{}.Read(context.Background(), readerRun("any-run"))
	assert.Nil(t, r)
	assert.True(t, errors.Is(err, ErrResultNotFound))
}

func TestRequeueOnConflict(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Group: "tests.kubetest.io", Resource: "testruns"},
		"r", errors.New("the object has been modified"))
	res, err := requeueOnConflict(ctrl.Result{}, conflict)
	assert.NoError(t, err, "conflicts are not reconcile errors")
	assert.Equal(t, conflictRequeue, res.RequeueAfter)

	other := errors.New("boom")
	_, err = requeueOnConflict(ctrl.Result{}, other)
	assert.ErrorIs(t, err, other, "other errors pass through")

	res, err = requeueOnConflict(ctrl.Result{RequeueAfter: 3}, nil)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{RequeueAfter: 3}, res)
}
