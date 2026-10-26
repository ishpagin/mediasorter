package setup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The CUDA build of ONNX Runtime and the NVIDIA libraries it needs (CUDA 12.8, cuDNN 9.8: the official
// redistributable wheels from PyPI), ~1.7 GB. Only for Linux with an NVIDIA driver: Windows uses DirectML,
// macOS CoreML, both shipped with the app.
var cudaFiles = []struct{ url, sha string }{
	{"https://github.com/microsoft/onnxruntime/releases/download/v1.24.4/onnxruntime-linux-x64-gpu-1.24.4.tgz",
		"c5f804ff5d239b436fa59e9f2fb288a39f7eb9552f6a636c8b71e792e91a8808"},
	{"https://files.pythonhosted.org/packages/0d/9b/a997b638fcd068ad6e4d53b8551a7d30fe8b404d6f1804abf1df69838932/nvidia_cuda_runtime_cu12-12.8.90-py3-none-manylinux2014_x86_64.manylinux_2_17_x86_64.whl",
		"adade8dcbd0edf427b7204d480d6066d33902cab2a4707dcfc48a2d0fd44ab90"},
	{"https://files.pythonhosted.org/packages/dc/61/e24b560ab2e2eaeb3c839129175fb330dfcfc29e5203196e5541a4c44682/nvidia_cublas_cu12-12.8.4.1-py3-none-manylinux_2_27_x86_64.whl",
		"8ac4e771d5a348c551b2a426eda6193c19aa630236b418086020df5ba9667142"},
	{"https://files.pythonhosted.org/packages/1f/13/ee4e00f30e676b66ae65b4f08cb5bcbb8392c03f54f2d5413ea99a5d1c80/nvidia_cufft_cu12-11.3.3.83-py3-none-manylinux2014_x86_64.manylinux_2_17_x86_64.whl",
		"4d2dd21ec0b88cf61b62e6b43564355e5222e4a3fb394cac0db101f2dd0d4f74"},
	{"https://files.pythonhosted.org/packages/fb/aa/6584b56dc84ebe9cf93226a5cde4d99080c8e90ab40f0c27bda7a0f29aa1/nvidia_curand_cu12-10.3.9.90-py3-none-manylinux_2_27_x86_64.whl",
		"b32331d4f4df5d6eefa0554c565b626c7216f87a06a4f56fab27c3b68a830ec9"},
	{"https://files.pythonhosted.org/packages/77/f0/8236c886a061d203e51247aec2b8e3a8f5350178251ab57237daf2140680/nvidia_cudnn_cu12-9.8.0.87-py3-none-manylinux_2_27_x86_64.whl",
		"d6b02cd0e3e24aa31d0193a8c39fec239354360d7d81055edddb69f35d53a4c8"},
}

// NVIDIA: Linux x64 with the NVIDIA driver loaded.
func NVIDIA() bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return false
	}
	_, err := os.Stat("/proc/driver/nvidia/version")
	return err == nil
}

// CUDAReady: the CUDA build is complete in dir; its ONNX Runtime is dir/libonnxruntime.so.
func CUDAReady(dir string) bool { return exists(filepath.Join(dir, ".complete")) }

// CUDA downloads the CUDA build into dir (resuming; each archive is checked and removed once unpacked).
func CUDA(ctx context.Context, dir string, progress Progress) error {
	if CUDAReady(dir) {
		return nil
	}
	for i, f := range cudaFiles {
		name := filepath.Base(f.url)
		done := filepath.Join(dir, name+".done")
		if exists(done) {
			continue
		}
		arch := filepath.Join(dir, name)
		lo := float64(i) / float64(len(cudaFiles))
		if err := fetch(ctx, f.url, arch, scaled(progress, "Видеокарта NVIDIA: CUDA", lo, lo+1/float64(len(cudaFiles)))); err != nil {
			return err
		}
		if err := checkSHA(arch, f.sha); err != nil {
			os.Remove(arch)
			return err
		}
		var err error
		if strings.HasSuffix(name, ".tgz") {
			err = untgzLibs(arch, dir)
		} else { // a wheel is a zip: nvidia/<lib>/lib/*.so*
			err = unzip(arch, dir, func(n string) string {
				if strings.HasPrefix(n, "nvidia/") && strings.Contains(n, "/lib/") && strings.Contains(n, ".so") &&
					!strings.Contains(n, "nvblas") { // NVBLAS (a BLAS drop-in) is not used and wants a config file
					return filepath.Base(n)
				}
				return ""
			})
		}
		if err != nil {
			return err
		}
		os.Remove(arch)
		if err := os.WriteFile(done, []byte("ok"), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, ".complete"), []byte("ok"), 0o644)
}

func checkSHA(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: файл повреждён (sha256 %s), скачаю заново", filepath.Base(path), got)
	}
	return nil
}

// untgzLibs: ONNX Runtime's lib/libonnxruntime.so.<version> (as libonnxruntime.so) and its CUDA providers.
func untgzLibs(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b := filepath.Base(h.Name)
		switch {
		case strings.HasPrefix(b, "libonnxruntime.so."):
			b = "libonnxruntime.so"
		case b == "libonnxruntime_providers_shared.so", b == "libonnxruntime_providers_cuda.so":
		default:
			continue
		}
		if err := writeFile(tr, filepath.Join(dir, b)); err != nil {
			return err
		}
	}
}

func writeFile(r io.Reader, dst string) error {
	tmp := dst + ".part"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
