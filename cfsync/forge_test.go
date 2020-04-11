package cfsync_test

import (
	"github.com/itzg/mc-server-runner/cfsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadLibrary_DefaultRepo(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.DownloadLibrary(outPath, "net.java.dev.jna:jna:4.4.0", "",
		[]string{"cb208278274bf12ebdb56c61bd7407e6f774d65a"})
	require.NoError(t, err)

	filePath := filepath.Join(outPath, "libraries", "net", "java", "dev", "jna", "jna", "4.4.0", "jna-4.4.0.jar")
	assert.FileExists(t, filePath)

	assertFileChecksum(t, filePath, "cb208278274bf12ebdb56c61bd7407e6f774d65a")
}

func TestDownloadLibrary_WrongChecksum(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.DownloadLibrary(outPath, "net.java.dev.jna:jna:4.4.0", "",
		[]string{"not correct at all"})
	assert.Error(t, err)

	assertDirEmpty(t, outPath)
}

func TestDownloadLibrary_IgnoreChecksum(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.DownloadLibrary(outPath, "net.java.dev.jna:jna:4.4.0", "",
		nil)
	require.NoError(t, err)

	filePath := filepath.Join(outPath, "libraries", "net", "java", "dev", "jna", "jna", "4.4.0", "jna-4.4.0.jar")
	assert.FileExists(t, filePath)

	assertFileChecksum(t, filePath, "cb208278274bf12ebdb56c61bd7407e6f774d65a")
}

func TestDownloadLibrary_ForgeRepo(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.DownloadLibrary(outPath,
		"org.jline:jline:3.5.1", "http://files.minecraftforge.net/maven/",
		[]string{"51800e9d7a13608894a5a28eed0f5c7fa2f300fb"},
	)
	require.NoError(t, err)

	filePath := filepath.Join(outPath, "libraries", "org", "jline", "jline", "3.5.1", "jline-3.5.1.jar")
	assert.FileExists(t, filePath)

	assertFileChecksum(t, filePath, "51800e9d7a13608894a5a28eed0f5c7fa2f300fb")
}

func TestDownloadLibrary_MultipleChecksums(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.DownloadLibrary(outPath,
		"com.typesafe.akka:akka-actor_2.11:2.3.3", "http://files.minecraftforge.net/maven/",
		[]string{"ed62e9fc709ca0f2ff1a3220daa8b70a2870078e",
			"25a86ccfdb6f6dfe08971f4825d0a01be83a6f2e"},
	)
	require.NoError(t, err)

	filePath := filepath.Join(outPath, "libraries", "com", "typesafe", "akka", "akka-actor_2.11", "2.3.3", "akka-actor_2.11-2.3.3.jar")
	assert.FileExists(t, filePath)

	assertFileChecksum(t, filePath, "ed62e9fc709ca0f2ff1a3220daa8b70a2870078e")
}

func TestDownloadLibrary_Exists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	dirPath := filepath.Join(outPath, "libraries", "net", "java", "dev", "jna", "jna", "4.4.0")
	err = os.MkdirAll(dirPath, 0755)
	require.NoError(t, err)
	filePath := filepath.Join(dirPath, "jna-4.4.0.jar")

	err = ioutil.WriteFile(filePath, []byte("This content is good enough for testing\n"), 0644)
	require.NoError(t, err)

	err = cfsync.DownloadLibrary(outPath, "net.java.dev.jna:jna:4.4.0", "",
		[]string{"cecd27477db0c7a32120dbccc0b1987a78c7d85a"})
	require.NoError(t, err)

	assert.FileExists(t, filePath)
	// and it left the content with ours
	assertFileChecksum(t, filePath, "cecd27477db0c7a32120dbccc0b1987a78c7d85a")
}

func TestDownloadLibrary_ExistsButNeedsRedownload(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	dirPath := filepath.Join(outPath, "libraries", "net", "java", "dev", "jna", "jna", "4.4.0")
	err = os.MkdirAll(dirPath, 0755)
	require.NoError(t, err)
	filePath := filepath.Join(dirPath, "jna-4.4.0.jar")

	ourFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = ourFile.WriteString("This content is good enough for testing\n")
	require.NoError(t, err)
	err = ourFile.Close()
	require.NoError(t, err)

	err = cfsync.DownloadLibrary(outPath, "net.java.dev.jna:jna:4.4.0", "",
		[]string{"cb208278274bf12ebdb56c61bd7407e6f774d65a"})
	require.NoError(t, err)

	assert.FileExists(t, filePath)
	// and it grabbed real content
	assertFileChecksum(t, filePath, "cb208278274bf12ebdb56c61bd7407e6f774d65a")
}

func TestPrepareLibrariesFromForgeVersionJson(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	file, err := os.Open(filepath.Join("testdata", "version.json"))
	require.NoError(t, err)
	defer file.Close()

	err = cfsync.PrepareLibrariesFromForgeVersionJson(outPath, file)
	require.NoError(t, err)

	assertFilesInDir(t, filepath.Join(outPath, "libraries"), []string{
		"akka-actor_2.11-2.3.3.jar", "asm-all-5.2.jar", "config-1.2.1.jar",
		"jline-3.5.1.jar", "jna-4.4.0.jar", "jopt-simple-5.0.3.jar",
		"launchwrapper-1.12.jar", "lzma-0.0.1.jar", "maven-artifact-3.5.3.jar",
		"scala-actors-migration_2.11-1.1.0.jar", "scala-compiler-2.11.1.jar",
		"scala-continuations-library_2.11-1.0.2.jar",
		"scala-continuations-plugin_2.11.1-1.0.2.jar",
		"scala-library-2.11.1.jar", "scala-parser-combinators_2.11-1.0.1.jar",
		"scala-reflect-2.11.1.jar", "scala-swing_2.11-1.0.1.jar",
		"scala-xml_2.11-1.0.2.jar", "trove4j-3.0.3.jar", "vecmath-1.5.2.jar",
	})

	assertFileChecksum(t, filepath.Join(outPath, "minecraft_server.1.12.2.jar"), "886945bfb2b978778c3a0288fd7fab09d315b25f")
}

func TestPrepareLibrariesForForge_Success(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.PrepareLibrariesForForge(filepath.Join("testdata", "version_only.jar"), outPath)
	require.NoError(t, err)

	assertFilesInDir(t, filepath.Join(outPath, "libraries"), []string{
		"akka-actor_2.11-2.3.3.jar", "asm-all-5.2.jar", "config-1.2.1.jar",
		"jline-3.5.1.jar", "jna-4.4.0.jar", "jopt-simple-5.0.3.jar",
		"launchwrapper-1.12.jar", "lzma-0.0.1.jar", "maven-artifact-3.5.3.jar",
		"scala-actors-migration_2.11-1.1.0.jar", "scala-compiler-2.11.1.jar",
		"scala-continuations-library_2.11-1.0.2.jar",
		"scala-continuations-plugin_2.11.1-1.0.2.jar",
		"scala-library-2.11.1.jar", "scala-parser-combinators_2.11-1.0.1.jar",
		"scala-reflect-2.11.1.jar", "scala-swing_2.11-1.0.1.jar",
		"scala-xml_2.11-1.0.2.jar", "trove4j-3.0.3.jar", "vecmath-1.5.2.jar",
	})
}

func TestPrepareLibrariesForForge_MissingVersionJson(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	err = cfsync.PrepareLibrariesForForge(filepath.Join("testdata", "missing_version_json.jar"), outPath)
	assert.Error(t, err)

	assertDirEmpty(t, outPath)
}

func TestDownloadMinecraftServer_NotExists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	serverPath := filepath.Join(outPath, "minecraft_server.1.12.2.jar")
	err = cfsync.DownloadMinecraftServer(outPath, "1.12.2")
	require.NoError(t, err)

	assertFileChecksum(t, serverPath, "886945bfb2b978778c3a0288fd7fab09d315b25f")
}

func TestDownloadMinecraftServer_Exists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	serverPath := filepath.Join(outPath, "minecraft_server.1.12.2.jar")
	err = ioutil.WriteFile(serverPath, []byte("This content is good enough for testing\n"), 0644)
	require.NoError(t, err)

	err = cfsync.DownloadMinecraftServer(outPath, "1.12.2")
	require.NoError(t, err)

	assertFileChecksum(t, serverPath, "cecd27477db0c7a32120dbccc0b1987a78c7d85a")
}

func TestDownloadForge(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	jarPath := filepath.Join(outPath, "forge-1.12.2-14.23.5.2847.jar")
	err = cfsync.DownloadForge(
		"https://modloaders.forgecdn.net/647622546/maven/net/minecraftforge/forge/1.12.2-14.23.5.2847/forge-1.12.2-14.23.5.2847.jar",
		jarPath,
	)
	require.NoError(t, err)

	assertFileChecksum(t, jarPath, "c15dbf708064a9db9a9d66dd84688b9f31b6006e")
}

func TestPrepareForge_NotExists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	jarPath := filepath.Join(outPath, "forge-1.12.2-14.23.5.2847.jar")

	filename, err := cfsync.PrepareForge(zap.NewNop(), "https://modloaders.forgecdn.net/647622546/maven/net/minecraftforge/forge/1.12.2-14.23.5.2847/forge-1.12.2-14.23.5.2847.jar", outPath)
	require.NoError(t, err)

	assert.Equal(t, "forge-1.12.2-14.23.5.2847.jar", filename)

	assertFileChecksum(t, jarPath, "c15dbf708064a9db9a9d66dd84688b9f31b6006e")

	assertFilesInDir(t, filepath.Join(outPath, "libraries"), []string{
		"akka-actor_2.11-2.3.3.jar", "asm-all-5.2.jar", "config-1.2.1.jar",
		"jline-3.5.1.jar", "jna-4.4.0.jar", "jopt-simple-5.0.3.jar",
		"launchwrapper-1.12.jar", "lzma-0.0.1.jar", "maven-artifact-3.5.3.jar",
		"scala-actors-migration_2.11-1.1.0.jar", "scala-compiler-2.11.1.jar",
		"scala-continuations-library_2.11-1.0.2.jar",
		"scala-continuations-plugin_2.11.1-1.0.2.jar",
		"scala-library-2.11.1.jar", "scala-parser-combinators_2.11-1.0.1.jar",
		"scala-reflect-2.11.1.jar", "scala-swing_2.11-1.0.1.jar",
		"scala-xml_2.11-1.0.2.jar", "trove4j-3.0.3.jar", "vecmath-1.5.2.jar",
	})

	assertFileChecksum(t, filepath.Join(outPath, "minecraft_server.1.12.2.jar"), "886945bfb2b978778c3a0288fd7fab09d315b25f")
}

func TestPrepareForge_Exists(t *testing.T) {
	outPath, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(outPath)

	jarPath := filepath.Join(outPath, "forge-1.12.2-14.23.5.2847.jar")

	err = copyFile(jarPath, filepath.Join("testdata", "version_only.jar"))

	filename, err := cfsync.PrepareForge(zap.NewNop(), "https://modloaders.forgecdn.net/647622546/maven/net/minecraftforge/forge/1.12.2-14.23.5.2847/forge-1.12.2-14.23.5.2847.jar", outPath)
	require.NoError(t, err)

	assert.Equal(t, "forge-1.12.2-14.23.5.2847.jar", filename)

	assertFileChecksum(t, jarPath, "ad5804bcbda6d21aa7b31f9ed254c03eb58ee6c2")

	assertFilesInDir(t, filepath.Join(outPath, "libraries"), []string{
		"akka-actor_2.11-2.3.3.jar", "asm-all-5.2.jar", "config-1.2.1.jar",
		"jline-3.5.1.jar", "jna-4.4.0.jar", "jopt-simple-5.0.3.jar",
		"launchwrapper-1.12.jar", "lzma-0.0.1.jar", "maven-artifact-3.5.3.jar",
		"scala-actors-migration_2.11-1.1.0.jar", "scala-compiler-2.11.1.jar",
		"scala-continuations-library_2.11-1.0.2.jar",
		"scala-continuations-plugin_2.11.1-1.0.2.jar",
		"scala-library-2.11.1.jar", "scala-parser-combinators_2.11-1.0.1.jar",
		"scala-reflect-2.11.1.jar", "scala-swing_2.11-1.0.1.jar",
		"scala-xml_2.11-1.0.2.jar", "trove4j-3.0.3.jar", "vecmath-1.5.2.jar",
	})

	assertFileChecksum(t, filepath.Join(outPath, "minecraft_server.1.12.2.jar"), "886945bfb2b978778c3a0288fd7fab09d315b25f")
}
