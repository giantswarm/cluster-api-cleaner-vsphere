
# Integration tests. These run locally only: CI runs "go test ./..." without a
# build tag, which skips them.
#
# This file is included automatically by the "include Makefile.*.mk" line in the
# root Makefile. The glob is alphabetical, so this file is read before
# Makefile.kubebuilder.mk. It therefore defines its own bin directory instead of
# relying on the GOBIN that file sets up later.

##@ Integration tests

INTEGRATION_GOBIN := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

# Matches k8s.io/api v0.36.x. Run "setup-envtest list" to see what is available.
ENVTEST_K8S_VERSION ?= 1.36.x
ENVTEST_VERSION ?= release-0.24
ENVTEST ?= $(INTEGRATION_GOBIN)/setup-envtest

.PHONY: setup-envtest
setup-envtest: ## Download setup-envtest locally if necessary.
	test -s $(ENVTEST) || go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: integration-test
integration-test: setup-envtest ## Run the integration tests against envtest and a vCenter simulator.
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -v -tags integration ./... -count=1
