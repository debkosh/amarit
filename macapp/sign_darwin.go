//go:build cgo

package macapp

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

// macapp_check checks the code at path as codesign --verify --deep --strict
// does and, with a requirement, that it meets it. It answers the status and
// the framework's words for a failure in msg. verdict says the status is the
// signature's own answer: unsigned, altered, or signed by someone else. Any
// other failure is a check that couldn't be made, such as a file that
// couldn't be read.
static OSStatus macapp_check(const char *path, const char *requirement, char *msg, size_t n, int *verdict) {
	*verdict = 0;
	msg[0] = 0;
	CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL, (const UInt8 *)path, strlen(path), true);
	if (url == NULL) return errSecParam;
	SecStaticCodeRef code = NULL;
	OSStatus s = SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, &code);
	CFRelease(url);
	if (s != errSecSuccess) return s;
	SecRequirementRef req = NULL;
	if (requirement != NULL) {
		CFStringRef text = CFStringCreateWithCString(NULL, requirement, kCFStringEncodingUTF8);
		s = SecRequirementCreateWithString(text, kSecCSDefaultFlags, &req);
		CFRelease(text);
		if (s != errSecSuccess) { CFRelease(code); return s; }
	}
	CFErrorRef err = NULL;
	s = SecStaticCodeCheckValidityWithErrors(code,
		kSecCSCheckAllArchitectures | kSecCSStrictValidate | kSecCSCheckNestedCode, req, &err);
	if (err != NULL) {
		CFStringRef d = CFErrorCopyDescription(err);
		if (d != NULL) { CFStringGetCString(d, msg, n, kCFStringEncodingUTF8); CFRelease(d); }
		CFRelease(err);
	}
	if (req != NULL) CFRelease(req);
	CFRelease(code);
	switch (s) {
	case errSecCSUnsigned:
	case errSecCSSignatureFailed:
	case errSecCSSignatureInvalid:
	case errSecCSSignatureNotVerifiable:
	case errSecCSSignatureUnsupported:
	case errSecCSReqFailed:
	case errSecCSBadResource:
	case errSecCSResourcesNotFound:
	case errSecCSResourcesNotSealed:
	case errSecCSResourcesInvalid:
	case errSecCSResourceRulesInvalid:
	case errSecCSBadObjectFormat:
	case errSecCSInfoPlistFailed:
	case errSecCSNoMainExecutable:
	case errSecCSBadBundleFormat:
	case errSecCSBadNestedCode:
	case errSecCSUnsealedAppRoot:
	case errSecCSUnsealedFrameworkRoot:
	case errSecCSInvalidSymlink:
	case errSecCSWeakResourceRules:
	case errSecCSWeakResourceEnvelope:
	case errSecCSStaticCodeChanged:
		*verdict = 1;
	}
	return s;
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/sudiptadeb/amarit"
)

var (
	// ErrWrongSigner is a bundle signed intact, by another key than the
	// requirement names.
	ErrWrongSigner = fmt.Errorf("not signed with the expected key: %w", amarit.ErrRefused)
	// ErrBadSignature is a bundle unsigned, or altered since it was signed.
	ErrBadSignature = fmt.Errorf("its signature is broken: %w", amarit.ErrRefused)
)

// SignedBy is the code requirement that the bundle was signed with the key
// whose certificate has this SHA-1, as a trust profile names it.
func SignedBy(leafSHA1 string) string {
	return fmt.Sprintf(`certificate leaf = H"%s"`, leafSHA1)
}

// Verify checks the bundle's signature, as `codesign --verify --deep
// --strict` does, and that it meets requirement when one is given. It asks
// the Security framework in this process rather than running codesign, a
// child that inherits the app's open files. A refusal wraps ErrWrongSigner
// or ErrBadSignature, and so amarit.ErrRefused; a check that couldn't be made
// wraps neither, and is worth trying again.
func Verify(app, requirement string) error {
	path := C.CString(app)
	defer C.free(unsafe.Pointer(path))
	var req *C.char
	if requirement != "" {
		req = C.CString(requirement)
		defer C.free(unsafe.Pointer(req))
	}
	msg := make([]byte, 512)
	var verdict C.int
	s := C.macapp_check(path, req, (*C.char)(unsafe.Pointer(&msg[0])), C.size_t(len(msg)), &verdict)
	if s == 0 {
		return nil
	}
	words := C.GoString((*C.char)(unsafe.Pointer(&msg[0])))
	if words == "" {
		words = fmt.Sprintf("status %d", int(s))
	}
	switch {
	case verdict == 0:
		return fmt.Errorf("the signature couldn't be checked: %s", words)
	case s == C.errSecCSReqFailed:
		return ErrWrongSigner
	default:
		return fmt.Errorf("%w (%s)", ErrBadSignature, words)
	}
}
