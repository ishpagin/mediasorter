package vision

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
static int load(const char *p) { return dlopen(p, RTLD_NOW | RTLD_GLOBAL) != NULL; }
*/
import "C"

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// preload: CUDA and cuDNN libraries lying next to the ONNX Runtime library (the desktop app downloads them
// there) are loaded before it, so the CUDA provider finds them without LD_LIBRARY_PATH.
func preload(lib string) {
	if lib == "" {
		return
	}
	dir := filepath.Dir(lib)
	var left []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if strings.Contains(n, ".so") && !strings.HasPrefix(n, "libonnxruntime") {
			left = append(left, filepath.Join(dir, n))
		}
	}
	// the libraries need each other: load in passes until nothing more loads
	for len(left) > 0 {
		var next []string
		for _, p := range left {
			cs := C.CString(p)
			ok := C.load(cs) != 0
			C.free(unsafe.Pointer(cs))
			if !ok {
				next = append(next, p)
			}
		}
		if len(next) == len(left) {
			return
		}
		left = next
	}
}
