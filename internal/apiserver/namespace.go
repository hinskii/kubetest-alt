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

package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// QueryNamespace is the query parameter that selects a namespace when the
// server runs cluster-wide.
const QueryNamespace = apiclient.QueryNamespace

// errBadRequest is a client error from request parsing (namespace
// resolution, query parameters) — always 400.
type errBadRequest struct{ msg string }

func (e errBadRequest) Error() string { return e.msg }

// targetNamespace resolves the namespace a single-object request (get,
// create, patch, delete, logs, artifacts) acts on. Candidates are the
// ?namespace= query parameter and, for create, the payload's
// metadata.namespace.
//
//   - Scoped server (Namespace set): every candidate must be empty or equal
//     to it. A mismatch is rejected instead of silently writing elsewhere —
//     the old code let a payload namespace win.
//   - Cluster-wide server (Namespace empty): exactly one namespace must be
//     named; candidates that disagree are rejected. Without this, name
//     lookups ran against Namespace "" and never found anything (fixes.md #2).
func (s *Server) targetNamespace(r *http.Request, payloadNS string) (string, error) {
	queryNS := r.URL.Query().Get(QueryNamespace)
	if s.Namespace != "" {
		for _, c := range []string{queryNS, payloadNS} {
			if c != "" && c != s.Namespace {
				return "", errBadRequest{fmt.Sprintf(
					"this API server is scoped to namespace %q; got %q", s.Namespace, c)}
			}
		}
		return s.Namespace, nil
	}
	switch {
	case queryNS != "" && payloadNS != "" && queryNS != payloadNS:
		return "", errBadRequest{fmt.Sprintf(
			"?namespace=%q disagrees with metadata.namespace %q", queryNS, payloadNS)}
	case queryNS != "":
		return queryNS, nil
	case payloadNS != "":
		return payloadNS, nil
	}
	return "", errBadRequest{"namespace is required: this API server is cluster-wide, pass ?namespace="}
}

// listNamespace resolves the namespace filter for list endpoints. Empty
// means "all namespaces" and is only possible on a cluster-wide server.
func (s *Server) listNamespace(r *http.Request) (string, error) {
	queryNS := r.URL.Query().Get(QueryNamespace)
	if s.Namespace != "" && queryNS != "" && queryNS != s.Namespace {
		return "", errBadRequest{fmt.Sprintf(
			"this API server is scoped to namespace %q; got %q", s.Namespace, queryNS)}
	}
	if s.Namespace != "" {
		return s.Namespace, nil
	}
	return queryNS, nil
}

// runRef is a run located either in the cluster (CR still exists) or only
// in the archive (store row). Keys are always derived from namespace + UID.
type runRef struct {
	Namespace string
	Name      string
	UID       string
	// CR is non-nil when the TestRun still exists in the cluster.
	CR *testsv1alpha1.TestRun
	// Row is non-nil when the run was found only in the store.
	Row *store.Row
}

func (ref runRef) keys() storage.RunKeys { return storage.ForRun(ref.Namespace, ref.UID) }

// errRunNotFound is returned by findRun when neither the cluster nor the
// store knows the id. Mapped to 404 by writeLookupError.
var errRunNotFound = errors.New("run not found")

// findRun resolves {id} to a run. The cluster is tried first with id as the
// CR name; then the store with id as the run UID. A non-UUID id never
// reaches the store (whose uid column would reject it with a 500).
func (s *Server) findRun(ctx context.Context, namespace, id string) (runRef, error) {
	var cr testsv1alpha1.TestRun
	err := s.K8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: id}, &cr)
	switch {
	case err == nil:
		return runRef{Namespace: cr.Namespace, Name: cr.Name, UID: string(cr.UID), CR: &cr}, nil
	case !apierrors.IsNotFound(err):
		return runRef{}, err
	}
	if s.Store == nil {
		return runRef{}, errRunNotFound
	}
	// Run history by UID, else by name: a finished TestRun leaves the
	// cluster (--finished-run-ttl) while links keep its name.
	var row *store.Row
	if _, perr := uuid.Parse(id); perr == nil {
		row, err = s.Store.Get(ctx, id)
	} else {
		row, err = s.Store.GetByName(ctx, namespace, id)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return runRef{}, errRunNotFound
		}
		return runRef{}, err
	}
	// The store is cluster-wide; never leak a row from another namespace.
	if row.Namespace != namespace {
		return runRef{}, errRunNotFound
	}
	return runRef{Namespace: row.Namespace, Name: row.Name, UID: row.UID, Row: row}, nil
}

// writeLookupError maps namespace/run-resolution errors to HTTP statuses
// and defers everything else to writeAPIError.
func writeLookupError(w http.ResponseWriter, err error) {
	var badReq errBadRequest
	switch {
	case errors.As(err, &badReq):
		writeError(w, http.StatusBadRequest, ReasonBadRequest, badReq.Error())
	case errors.Is(err, errRunNotFound):
		writeError(w, http.StatusNotFound, ReasonNotFound, err.Error())
	default:
		writeAPIError(w, err)
	}
}
