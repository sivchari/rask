//go:build darwin

package vz

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>

// raskCheckVirtualizationEntitlement inspects the calling process's own
// code signature (SecCodeCopySelf — no path/PID lookup needed, this always
// means "us") for the com.apple.security.virtualization boolean
// entitlement.
//
// Returns 1 if the entitlement is present and true, 0 if the signing
// information was read successfully but the entitlement is absent (or
// present-but-false), and -1 if signing information could not be read at
// all (SecCodeCopySelf/SecCodeCopySigningInformation failed) — the caller
// treats -1 as "unknown, fail open" rather than "absent", since an
// unexpected Security.framework error is not proof the entitlement is
// missing.
static int raskCheckVirtualizationEntitlement(void) {
	SecCodeRef code = NULL;
	if (SecCodeCopySelf(kSecCSDefaultFlags, &code) != errSecSuccess || code == NULL) {
		return -1;
	}

	CFDictionaryRef info = NULL;
	OSStatus status = SecCodeCopySigningInformation((SecStaticCodeRef)code, kSecCSSigningInformation, &info);
	CFRelease(code);

	if (status != errSecSuccess || info == NULL) {
		return -1;
	}

	int result = 0; // signing info read fine; default to "absent" below.

	CFDictionaryRef entitlements = (CFDictionaryRef)CFDictionaryGetValue(info, kSecCodeInfoEntitlementsDict);
	if (entitlements != NULL) {
		CFBooleanRef val = (CFBooleanRef)CFDictionaryGetValue(entitlements, CFSTR("com.apple.security.virtualization"));
		if (val != NULL && CFBooleanGetValue(val)) {
			result = 1;
		}
	}

	CFRelease(info);

	return result;
}
*/
import "C"

import (
	"fmt"
	"os"
)

// Values raskCheckVirtualizationEntitlement (and entitlementProbe below)
// return; kept as named constants so checkVirtualizationEntitlement's
// switch reads as intent rather than magic numbers.
const (
	entitlementUnknown = -1 // couldn't be determined; fail open
	entitlementAbsent  = 0  // definitively not present
	entitlementPresent = 1
)

// entitlementProbe queries this process's own code signature for the
// com.apple.security.virtualization entitlement, returning one of the
// entitlement* constants above. A package variable (not called directly)
// so tests can substitute a synthetic result without depending on this
// test binary's own real ad-hoc signature.
var entitlementProbe = func() int {
	return int(C.raskCheckVirtualizationEntitlement())
}

// checkVirtualizationEntitlement fails fast, before any Virtualization.
// framework work happens, if the currently running executable is not
// signed with the com.apple.security.virtualization entitlement that
// Virtualization.framework requires to start a VM at all.
//
// Without this, an unsigned/unentitled binary's VM never becomes usable:
// vm.Start() itself reports success, but the guest never boots, so the
// caller only finds out ~bootTimeout later via a generic "waiting for
// guest agent to become healthy: context deadline exceeded" — a real,
// hours-burning trap during local development (`go build`/`make build`
// produce an unentitled binary; only `make codesign`/goreleaser sign one).
//
// Fails open on anything other than a definitive "absent": an unexpected
// error reading signing information (entitlementUnknown) or a failure to
// resolve this process's own executable path could just as easily mean a
// working setup hit an unrelated Security.framework or OS hiccup, and this
// check must never turn that into a false failure.
func checkVirtualizationEntitlement() error {
	if entitlementProbe() != entitlementAbsent {
		return nil
	}

	path, err := os.Executable()
	if err != nil {
		return nil
	}

	return entitlementError(path)
}

// entitlementError formats the fix instructions for path, split out from
// checkVirtualizationEntitlement so the message itself is testable without
// invoking cgo or depending on this process's real code signature.
func entitlementError(path string) error {
	return fmt.Errorf(
		"vz: this binary is not signed with the com.apple.security.virtualization entitlement, so Virtualization.framework cannot start a VM.\n"+
			"Sign it with: codesign --entitlements vz.entitlements -f -s - %s\n"+
			"(or build via `make codesign`, which does this for you)",
		path,
	)
}
