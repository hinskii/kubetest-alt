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

package compiler

// ExecutorDefaults describes the built-in wrapper defaults per executor type.
//
// The distribution model at step 03: the operator schedules pods running the
// upstream tool image directly. Step 05/11 replaces `Image` with our own
// wrapper image (upstream + /entry binary) — the map shape stays the same, so
// the compiler and the rest of the pipeline don't change.
type ExecutorDefaults struct {
	// Image is the container image reference (repo[:tag]).
	Image string

	// Command runs before Args in the container. Empty means "inherit the
	// image's ENTRYPOINT" — step 05 wrappers will set this to /entry.
	Command []string

	// Args are default arguments to the tool inside the wrapper. Callers can
	// override with Test.spec.container.args.
	Args []string
}

// Executor type identifiers. These are the values TestSpec.Type is validated
// against by the webhook enum and by resolveExecutor here.
const (
	ExecutorK6      = "k6"
	ExecutorCypress = "cypress"
	ExecutorNewman  = "newman"
	ExecutorLocust  = "locust"
	ExecutorJMeter  = "jmeter"
)

// DefaultExecutorImages pins the upstream image + default invocation for each
// executor type. Tags verified 2026-08-17 via github.com and Docker Hub APIs.
//
// This is a public var (not a const map — Go has no const maps) so the future
// operator config can rewrite entries at startup:
//
//	compiler.DefaultExecutorImages["k6"] = compiler.ExecutorDefaults{
//	    Image: "mycorp.registry/mirror/grafana/k6:2.2.0", ...
//	}
//
// See Options.ImageRegistry for the simpler "just prefix every image" hook.
//
// Note on jmeter: CLAUDE.md §11 mentions justb4/jmeter which last shipped in
// 2022 on JMeter 5.5. alpine/jmeter tracks upstream (5.6.3 pushed 2026-08-15)
// and is a healthier default. Step 11 revisits this when we build our own
// wrappers.
// The strings inside Args are per-tool CLI vocabulary (k6's "run" is not
// cypress's "run" — extracting a shared const would obscure intent), so this
// map suppresses goconst for the args literals.
//
//nolint:goconst // per-tool CLI vocabulary; extraction would obscure intent
var DefaultExecutorImages = map[string]ExecutorDefaults{
	ExecutorK6: {
		Image:   "grafana/k6:2.2.0",
		Command: nil,
		Args:    []string{"run", "--summary-export=summary.json", "script.js"},
	},
	ExecutorCypress: {
		Image:   "cypress/included:15.20.1",
		Command: nil,
		Args:    []string{"cypress", "run", "--reporter", "junit"},
	},
	ExecutorNewman: {
		Image:   "postman/newman:6.2.2",
		Command: nil,
		Args:    []string{"run", "coll.json", "-r", "cli,json,junit"},
	},
	ExecutorLocust: {
		Image:   "locustio/locust:2.46.3",
		Command: nil,
		Args:    []string{"--headless", "--csv=out", "-f", "locustfile.py"},
	},
	ExecutorJMeter: {
		Image:   "alpine/jmeter:5.6.3",
		Command: nil,
		Args:    []string{"-n", "-t", "plan.jmx", "-l", "out.jtl", "-e", "-o", "report"},
	},
}

// resolveExecutor returns the effective image + argv for a Test, honoring
// Options.ExecutorImages overrides, Options.ImageRegistry prefix, and per-Test
// container overrides. Ordering matters: (defaults ⊕ options overrides) then
// registry prefix then per-Test overrides.
func resolveExecutor(testType string, opts Options, testContainerImage string,
	testCommand []string, testArgs []string) (image string, command []string, args []string, ok bool) {
	base, ok := DefaultExecutorImages[testType]
	if !ok {
		return "", nil, nil, false
	}

	image = base.Image
	command = append([]string(nil), base.Command...)
	args = append([]string(nil), base.Args...)

	if override, hasOverride := opts.ExecutorImages[testType]; hasOverride && override != "" {
		image = override
	}
	if opts.ImageRegistry != "" {
		image = opts.ImageRegistry + "/" + image
	}
	if testContainerImage != "" {
		// Per-Test image override wins over both defaults and Options overrides.
		// Intentionally NOT re-prefixed with ImageRegistry — the user-supplied
		// image is treated as fully qualified.
		image = testContainerImage
	}
	if len(testCommand) > 0 {
		command = append([]string(nil), testCommand...)
	}
	if len(testArgs) > 0 {
		args = append([]string(nil), testArgs...)
	}
	return image, command, args, true
}
