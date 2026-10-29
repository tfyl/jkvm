// Package e2e runs the jkvm binary against a real JetKVM. The tests are
// behind the e2e build tag and skip unless JKVM_E2E_HOST is set:
//
//	JKVM_E2E_HOST=http://jetkvm.local go test -tags e2e -count=1 ./e2e/...
//
// The default tier only reads from the device. JKVM_E2E_INPUT=1 adds a tier
// that sends keyboard, mouse and virtual media input to the target machine;
// see CONTRIBUTING.md before running it.
package e2e
