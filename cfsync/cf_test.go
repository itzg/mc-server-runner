package cfsync_test

import (
	"github.com/itzg/mc-server-runner/cfsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMinecraftInstance(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "minecraftinstance.json"))
	require.NoError(t, err)
	defer file.Close()

	instance, err := cfsync.LoadMinecraftInstance(file)
	require.NoError(t, err)

	assert.NotNil(t, instance)
	assert.Equal(t, "https://modloaders.forgecdn.net/647622546/maven/net/minecraftforge/forge/1.15.2-31.1.27/forge-1.15.2-31.1.27.jar",
		instance.BaseModLoader.DownloadUrl)
	assert.Equal(t, "Valhelsia 2", instance.Name)

	assert.Len(t, instance.InstalledAddons, 122)

	assert.Equal(t, "Bookshelf-1.15.2-5.1.4.jar",
		instance.InstalledAddons[0].InstalledFile.FileNameOnDisk)
	assert.Equal(t, "https://edge.forgecdn.net/files/2898/277/Bookshelf-1.15.2-5.1.4.jar",
		instance.InstalledAddons[0].InstalledFile.DownloadUrl)

	assert.Equal(t, "AmbientSounds_v3.0.19_mc1.15.2.jar",
		instance.InstalledAddons[121].InstalledFile.FileNameOnDisk)
	assert.Equal(t, "https://edge.forgecdn.net/files/2905/243/AmbientSounds_v3.0.19_mc1.15.2.jar",
		instance.InstalledAddons[121].InstalledFile.DownloadUrl)
}

func TestPrepareModFile_NotExists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not really jar content"))
	}))
	defer ts.Close()

	modPath := filepath.Join(outPath, "mod.jar")
	err = cfsync.PrepareModFile(zap.NewNop(), modPath, ts.URL)
	require.NoError(t, err)

	assertFileContent(t, modPath, "not really jar content")
}

func TestPrepareModFile_Exists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	serverCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		_, _ = w.Write([]byte("new content"))
	}))
	defer ts.Close()

	modPath := filepath.Join(outPath, "mod.jar")

	err = ioutil.WriteFile(modPath, []byte("old content"), 0644)
	require.NoError(t, err)

	err = cfsync.PrepareModFile(nil, modPath, ts.URL)
	require.NoError(t, err)

	assert.False(t, serverCalled)
	assertFileContent(t, modPath, "old content")
}

func TestPrepareModFile_BadDownload(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	modPath := filepath.Join(outPath, "mod.jar")

	err = cfsync.PrepareModFile(zap.NewNop(), modPath, ts.URL)
	require.Error(t, err)
}

func TestPrepareMods(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer ts.Close()

	err = os.MkdirAll(filepath.Join(outPath, cfsync.ModsSubdir), 0755)
	require.NoError(t, err)

	oldJarPath := filepath.Join(outPath, cfsync.ModsSubdir, "mod_old.jar")
	err = ioutil.WriteFile(oldJarPath, []byte("old content"), 0644)
	require.NoError(t, err)

	oldFilePath := filepath.Join(outPath, cfsync.ModsSubdir, "testing.txt")
	err = ioutil.WriteFile(oldFilePath, []byte("old content"), 0644)
	require.NoError(t, err)

	// ...and an existing directory, should be ignored
	oldDirPath := filepath.Join(outPath, "ignoreThisDir")
	err = os.Mkdir(oldDirPath, 0755)
	require.NoError(t, err)

	var instance cfsync.CfMinecraftInstance
	instance.InstalledAddons = []*cfsync.CfInstalledAddon{
		{
			InstalledFile: cfsync.CfInstalledAddonFile{
				DownloadUrl:    ts.URL + "/mod1.jar",
				FileNameOnDisk: "mod1.jar",
			},
		},
		{
			InstalledFile: cfsync.CfInstalledAddonFile{
				DownloadUrl:    ts.URL + "/mod2.jar",
				FileNameOnDisk: "mod2.jar",
			},
		},
	}

	err = cfsync.PrepareMods(zap.NewNop(), &instance, outPath)
	require.NoError(t, err)

	assertFileContent(t, filepath.Join(outPath, cfsync.ModsSubdir, "mod1.jar"), "/mod1.jar")
	assertFileContent(t, filepath.Join(outPath, cfsync.ModsSubdir, "mod2.jar"), "/mod2.jar")

	// old jar removed
	exists, err := cfsync.FileExists(oldJarPath)
	require.NoError(t, err)
	assert.False(t, exists)

	// old non-jar remains
	exists, err = cfsync.FileExists(oldFilePath)
	require.NoError(t, err)
	assert.True(t, exists)

	// and directory still there
	exists, err = cfsync.FileExists(oldDirPath)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestPrepareInstance(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	file, err := os.Open(filepath.Join("testdata", "minecraftinstance_small.json"))
	require.NoError(t, err)

	serverJar, err := cfsync.PrepareInstance(zap.NewNop(), file, outPath)
	file.Close()
	require.NoError(t, err)

	assert.Equal(t, "forge-1.12.2-14.23.5.2847.jar", serverJar)

	assertFileChecksum(t, filepath.Join(outPath, "forge-1.12.2-14.23.5.2847.jar"),
		"c15dbf708064a9db9a9d66dd84688b9f31b6006e")
	assertFileChecksum(t, filepath.Join(outPath, "minecraft_server.1.12.2.jar"),
		"886945bfb2b978778c3a0288fd7fab09d315b25f")

	assertFileChecksum(t, filepath.Join(outPath, cfsync.ModsSubdir, "walljump-1.12.2-1.2.3.jar"),
		"441748846546b2fb3b711d08bcc740de3cb2f242")

	assertFilesInDir(t, filepath.Join(outPath, cfsync.ForgeLibrariesSubpath), []string{
		"akka-actor_2.11-2.3.3.jar", "asm-all-5.2.jar", "config-1.2.1.jar", "jline-3.5.1.jar",
		"jna-4.4.0.jar", "jopt-simple-5.0.3.jar", "launchwrapper-1.12.jar", "lzma-0.0.1.jar",
		"maven-artifact-3.5.3.jar", "scala-actors-migration_2.11-1.1.0.jar", "scala-compiler-2.11.1.jar",
		"scala-continuations-library_2.11-1.0.2.jar", "scala-continuations-plugin_2.11.1-1.0.2.jar",
		"scala-library-2.11.1.jar", "scala-parser-combinators_2.11-1.0.1.jar",
		"scala-reflect-2.11.1.jar", "scala-swing_2.11-1.0.1.jar", "scala-xml_2.11-1.0.2.jar",
		"trove4j-3.0.3.jar", "vecmath-1.5.2.jar",
	})
}
