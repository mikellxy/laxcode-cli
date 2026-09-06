package workfs

// workfs 的测试覆盖真实磁盘语义：领域层依赖的错误分类契约
// （errors.Is(err, fs.ErrNotExist)）、父目录自动创建与权限位。
// 分页读取算法本身的测试在 domain/tools/pagedread_test.go（纯内存）。

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdataPath 是仓库自带测试资源：4 行、每行 11 字符、末行无换行符。
const testdataPath = "./testdata/test_data.txt"

const testdataContent = "11111111111\n22222222222\n33333333333\n44444444444"

func TestOpenReadReadsFixture(t *testing.T) {
	f, err := New().OpenRead(testdataPath)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(b) != testdataContent {
		t.Errorf("content = %q, want %q", b, testdataContent)
	}
}

// TestOpenReadMissingFileIsErrNotExist 锁定领域层的错误分类契约：
// read_file / edit_file 靠 errors.Is(err, fs.ErrNotExist) 区分"文件不存在"
// 与其他 IO 失败，从而给模型不同的自愈提示。
func TestOpenReadMissingFileIsErrNotExist(t *testing.T) {
	_, err := New().OpenRead(filepath.Join(t.TempDir(), "no_such_file.txt"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, fs.ErrNotExist) == true", err)
	}
}

func TestReadFileMissingIsErrNotExist(t *testing.T) {
	_, err := New().ReadFile(filepath.Join(t.TempDir(), "no_such_file.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, fs.ErrNotExist) == true", err)
	}
}

func TestReadFileReturnsContent(t *testing.T) {
	b, err := New().ReadFile(testdataPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != testdataContent {
		t.Errorf("content = %q, want %q", b, testdataContent)
	}
}

func TestWriteFileCreatesMissingParentDirs(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c", "out.txt")

	if err := New().WriteFile(target, []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// 权限位受 umask 影响，只断言属主可读写
	if info.Mode().Perm()&0o600 != 0o600 {
		t.Errorf("perm = %v, want owner read+write", info.Mode().Perm())
	}
}

func TestWriteFileOverwritesExisting(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(target, []byte("old content longer"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New().WriteFile(target, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

// TestWriteFileParentDirError 覆盖父目录无法创建时的错误包装：
// 路径上已存在同名普通文件，MkdirAll 必然失败。
func TestWriteFileParentDirError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	err := New().WriteFile(filepath.Join(blocker, "sub", "out.txt"), []byte("hello"))
	if err == nil {
		t.Fatal("expected error when parent dir cannot be created")
	}
	if !strings.Contains(err.Error(), "create parent dir") {
		t.Errorf("err = %v, want it to mention %q", err, "create parent dir")
	}
}
