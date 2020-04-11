package cfsync_test

import (
	"crypto/sha1"
	"fmt"
	"github.com/karrick/godirwalk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func assertFileContent(t *testing.T, path string, expectedContent string) {
	content, err := ioutil.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, expectedContent, string(content))
}

func assertDirEmpty(t *testing.T, path string) {
	count := 0
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			count++
			t.Log(info.Name())
		}

		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 0, count)
}

func copyFile(destPath string, srcPath string) error {
	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer destFile.Close()

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return err
	}

	return nil
}

func assertFileChecksum(t *testing.T, filePath string, expectedSha1 string) {
	file, err := os.Open(filePath)
	require.NoError(t, err)

	h := sha1.New()
	_, err = io.Copy(h, file)
	require.NoError(t, err)

	assert.Equal(t, expectedSha1, fmt.Sprintf("%x", h.Sum(nil)))
}

func assertFilesInDir(t *testing.T, dirPath string, expected []string) {

	names := make([]string, 0)

	err := godirwalk.Walk(dirPath, &godirwalk.Options{
		Callback: func(osPathname string, e *godirwalk.Dirent) error {
			if e.IsRegular() {
				names = append(names, e.Name())
			}
			return nil
		},
	})
	require.NoError(t, err)

	sort.Strings(names)
	sort.Strings(expected)

	assert.Equal(t, expected, names)
}
