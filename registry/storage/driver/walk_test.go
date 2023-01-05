package driver

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type changingFileSystem struct {
	StorageDriver
	fileset   []string
	keptFiles map[string]bool
}

func (cfs *changingFileSystem) List(_ context.Context, _ string) ([]string, error) {
	return cfs.fileset, nil
}

func (cfs *changingFileSystem) Stat(_ context.Context, path string) (FileInfo, error) {
	kept, ok := cfs.keptFiles[path]
	if ok && kept {
		return &FileInfoInternal{
			FileInfoFields: FileInfoFields{
				Path: path,
			},
		}, nil
	}
	return nil, PathNotFoundError{}
}

type fileSystem struct {
	StorageDriver
	// maps folder to list results
	fileset map[string][]string
}

func (cfs *fileSystem) List(_ context.Context, path string) ([]string, error) {
	return cfs.fileset[path], nil
}

func (cfs *fileSystem) Stat(_ context.Context, path string) (FileInfo, error) {
	_, isDir := cfs.fileset[path]
	return &FileInfoInternal{
		FileInfoFields: FileInfoFields{
			Path:  path,
			IsDir: isDir,
			Size:  int64(len(path)),
		},
	}, nil
}

func (cfs *fileSystem) isDir(path string) bool {
	_, isDir := cfs.fileset[path]
	return isDir
}

func TestWalkFileRemoved(t *testing.T) {
	d := &changingFileSystem{
		fileset: []string{"zoidberg", "bender"},
		keptFiles: map[string]bool{
			"zoidberg": true,
		},
	}
	infos := []FileInfo{}
	err := WalkFallback(context.Background(), d, "", func(fileInfo FileInfo) error {
		infos = append(infos, fileInfo)
		return nil
	})
	if len(infos) != 1 || infos[0].Path() != "zoidberg" {
		t.Errorf(fmt.Sprintf("unexpected path set during walk: %s", infos))
	}
	if err != nil {
		t.Fatalf(err.Error())
	}
}

func TestWalkFallback(t *testing.T) {
	d := &fileSystem{
		fileset: map[string][]string{
			"/":        {"/file1", "/folder1", "/folder2"},
			"/folder1": {"/folder1/file1"},
			"/folder2": {"/folder2/file1"},
		},
	}
	expected := []string{
		"/file1",
		"/folder1",
		"/folder1/file1",
		"/folder2",
		"/folder2/file1",
	}

	var walked []FileInfo
	err := WalkFallback(context.Background(), d, "/", func(fileInfo FileInfo) error {
		if fileInfo.IsDir() != d.isDir(fileInfo.Path()) {
			t.Fatalf("fileInfo isDir not matching file system: expected %t actual %t", d.isDir(fileInfo.Path()), fileInfo.IsDir())
		}
		walked = append(walked, fileInfo)
		return nil
	})
	if err != nil {
		t.Fatalf(err.Error())
	}
	if expected != len(walked) {
		t.Fatalf("mismatch number of fileInfo walked, expected %d", expected)
	}
}

// Walk is expected to skip directory on ErrSkipDir
func TestWalkFallbackSkipDirOnDir(t *testing.T) {
	d := &fileSystem{
		fileset: map[string][]string{
			"/":        {"/file1", "/folder1", "/folder2"},
			"/folder1": {"/folder1/file1"}, // should not be walked
			"/folder2": {"/folder2/file1"},
		},
	}
	skipDir := "/folder1"
	expected := []string{
		"/file1",
		"/folder1", // return ErrSkipDir, skip anything under /folder1
		// skip /folder1/file1
		"/folder2",
		"/folder2/file1",
	}

	var walked []string
	err := WalkFallback(context.Background(), d, "/", func(fileInfo FileInfo) error {
		walked = append(walked, fileInfo.Path())
		if fileInfo.Path() == skipDir {
			return ErrSkipDir
		}
		if strings.Contains(fileInfo.Path(), skipDir) {
			t.Fatalf("skipped dir %s and should not walk %s", skipDir, fileInfo.Path())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected Walk to not error %v", err)
	}
	compareWalked(t, expected, walked)
}

func TestWalkFallbackSkipDirOnFile(t *testing.T) {
	d := &fileSystem{
		fileset: map[string][]string{
			"/": {"/file1", "/file2", "/file3"},
		},
	}
	skipFile := "/file2"
	expected := []string{
		"/file1",
		"/file2", // return ErrSkipDir, stop early
	}

	var walked []string
	err := WalkFallback(context.Background(), d, "/", func(fileInfo FileInfo) error {
		walked = append(walked, fileInfo.Path())
		if fileInfo.Path() == skipFile {
			return ErrSkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected Walk to not error %v", err)
	}
	compareWalked(t, expected, walked)
}

// Walk is expected to skip directory on ErrSkipDir
func TestWalkFallbackErr(t *testing.T) {
	d := &fileSystem{
		fileset: map[string][]string{
			"/": {"/file1", "/file2", "/file3"},
		},
	}
	errFile := "/file2"
	expected := []string{
		"/file1",
		"/file2", // return ErrSkipDir, stop early
	}
	expectedErr := errors.New("foo")

	var walked []string
	err := WalkFallback(context.Background(), d, "/", func(fileInfo FileInfo) error {
		walked = append(walked, fileInfo.Path())
		if fileInfo.Path() == errFile {
			return expectedErr
		}
		return nil
	})
	if err != expectedErr {
		t.Fatalf("unexpected err %v", err)
	}
	compareWalked(t, expected, walked)
}

func compareWalked(t *testing.T, expected, walked []string) {
	if len(walked) != len(expected) {
		t.Fatalf("Mismatch number of fileInfo walked %d expected %d", len(walked), len(expected))
	}
	for i := range walked {
		if walked[i] != expected[i] {
			t.Fatalf("expected walked to come in order expected: walked %s", walked)
		}
	}
}
