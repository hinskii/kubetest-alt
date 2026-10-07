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
// Package podpolicy is what a test pod may ask for (fixes.md #1).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// EnvToken lets a pipeline pass the API token without reading the Secret.
const EnvToken = "KUBETEST_API_TOKEN" // #nosec G101 -- an environment variable's name

// apiSelector finds the chart's API server Service.
const apiSelector = "app.kubernetes.io/name=kubetest-alt,app.kubernetes.io/component=apiserver"

// Connect opens a Connection from the kubeconfig, like kubectl does.
func Connect(ctx context.Context, f *GlobalFlags) (*Connection, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if f.Kubeconfig != "" {
		rules.ExplicitPath = f.Kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: f.Context})
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	ns := f.Namespace
	if ns == "" {
		if ns, _, err = cc.Namespace(); err != nil {
			return nil, fmt.Errorf("kubeconfig namespace: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	ep, err := discover(ctx, cs, f)
	if err != nil {
		return nil, err
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		return nil, err
	}
	api, err := apiclient.New(apiclient.ServiceProxyURL(rc.Host, f.KubetestNamespace, ep.service, ep.port), hc)
	if err != nil {
		return nil, err
	}
	return &Connection{API: api.WithToken(ep.token).AsUser(cliUser()), Namespace: ns}, nil
}

// endpoint is where the API is and how to get in.
type endpoint struct {
	service string
	port    int
	token   string
}

// discover finds the API server Service and its token in the kubetest
// namespace (flags override both).
func discover(ctx context.Context, cs kubernetes.Interface, f *GlobalFlags) (endpoint, error) {
	var svc *corev1.Service
	if f.APIService != "" {
		s, err := cs.CoreV1().Services(f.KubetestNamespace).Get(ctx, f.APIService, metav1.GetOptions{})
		if err != nil {
			return endpoint{}, fmt.Errorf("kubetest API Service %s/%s: %w", f.KubetestNamespace, f.APIService, err)
		}
		svc = s
	} else {
		list, err := cs.CoreV1().Services(f.KubetestNamespace).List(ctx, metav1.ListOptions{LabelSelector: apiSelector})
		if err != nil {
			return endpoint{}, fmt.Errorf("finding the kubetest API in namespace %s: %w", f.KubetestNamespace, err)
		}
		switch len(list.Items) {
		case 0:
			return endpoint{}, fmt.Errorf("no kubetest API Service in namespace %s — is kubetest installed there? (--kubetest-namespace)",
				f.KubetestNamespace)
		case 1:
			svc = &list.Items[0]
		default:
			return endpoint{}, fmt.Errorf("several kubetest API Services in namespace %s — pick one with --api-service", f.KubetestNamespace)
		}
	}
	ep := endpoint{service: svc.Name, port: servicePort(svc)}
	if tok := strings.TrimSpace(os.Getenv(EnvToken)); tok != "" {
		ep.token = tok
		return ep, nil
	}
	secret := f.TokenSecret
	if secret == "" {
		// The chart's naming: <fullname>-apiserver ↔ <fullname>-api-token.
		secret = strings.TrimSuffix(svc.Name, "-apiserver") + "-api-token"
	}
	s, err := cs.CoreV1().Secrets(f.KubetestNamespace).Get(ctx, secret, metav1.GetOptions{})
	switch {
	case apierrors.IsForbidden(err):
		return endpoint{}, fmt.Errorf("reading the API token (Secret %s/%s) is forbidden for you: ask for get on it, or set $%s",
			f.KubetestNamespace, secret, EnvToken)
	case apierrors.IsNotFound(err):
		return endpoint{}, fmt.Errorf("no API token Secret %s/%s — pass --api-token-secret or set $%s", f.KubetestNamespace, secret, EnvToken)
	case err != nil:
		return endpoint{}, fmt.Errorf("API token Secret %s/%s: %w", f.KubetestNamespace, secret, err)
	}
	tok := strings.TrimSpace(string(s.Data["token"]))
	if tok == "" {
		return endpoint{}, errors.New("the API token Secret has no \"token\" key")
	}
	ep.token = tok
	return ep, nil
}

func servicePort(s *corev1.Service) int {
	for _, p := range s.Spec.Ports {
		if p.Name == "http" {
			return int(p.Port)
		}
	}
	if len(s.Spec.Ports) > 0 {
		return int(s.Spec.Ports[0].Port)
	}
	return 8080
}

// cliUser attributes CLI runs (created-by): $KUBETEST_USER, else the OS
// user — informative only; the token is what grants access.
func cliUser() string {
	if u := os.Getenv("KUBETEST_USER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u + " (cli)"
	}
	return "cli"
}
