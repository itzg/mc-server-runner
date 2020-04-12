package cfsync

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	mainLibrariesRepo     = "https://libraries.minecraft.net/"
	ForgeLibrariesSubpath = "libraries"
)

var (
	minecraftServerUrl = template.Must(template.New("minecraftServerUrl").Parse(
		"https://s3.amazonaws.com/Minecraft.Download/versions/{{.Version}}/minecraft_server.{{.Version}}.jar"))
)

type minecraftServerUrlContext struct {
	Version string
}

type ForgeVersion struct {
	// InheritsFrom is the base minecraft server version
	InheritsFrom string
	Libraries    []struct {
		Name      string
		Url       string
		Serverreq bool
		Checksums []string
	}
}

// PrepareForge ensures the version of forge referenced by the given URL is available in the basePath.
// Returns the filename of the forge server jar (relative to the basePath)
func PrepareForge(logger *zap.Logger, forgeUrl string, basePath string) (string, error) {
	logger.Info("Preparing forge server")

	parsedUrl, err := url.Parse(forgeUrl)
	if err != nil {
		return "", fmt.Errorf("invalid forge URL: %w", err)
	}

	_, filename := path.Split(parsedUrl.Path)

	forgeFilePath := filepath.Join(basePath, filename)
	exists, err := FileExists(forgeFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to check if forge jar already exists: %w", err)
	}
	if !exists {
		logger.Debug("Downloading forge", zap.String("url", forgeUrl))
		err = DownloadForge(forgeUrl, forgeFilePath)
		if err != nil {
			return "", fmt.Errorf("failed to download forge from %s: %w", forgeUrl, err)
		}
	}

	logger.Info("Preparing libraries for forge server")
	err = PrepareLibrariesForForge(forgeFilePath, basePath)
	if err != nil {
		return "", fmt.Errorf("failed to prepare libraries: %w", err)
	}

	return filename, nil
}

func DownloadForge(forgeUrl string, outFilePath string) error {
	resp, err := http.Get(forgeUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return buildDownloadError(forgeUrl, resp)
	}

	file, err := os.OpenFile(outFilePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open forge jar file for writing : %w", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write forge jar content: %w", err)
	}

	return nil
}

func PrepareLibrariesForForge(forgeJarPath string, basePath string) error {
	r, err := zip.OpenReader(forgeJarPath)
	if err != nil {
		return fmt.Errorf("unable to open forge jar: %w", err)
	}
	defer r.Close()

	found := false
	for _, f := range r.File {
		if f.Name == "version.json" {
			found = true
			versionJsonReader, err := f.Open()
			if err != nil {
				return fmt.Errorf("failed to open version.json from jar: %w", err)
			}

			err = PrepareLibrariesFromForgeVersionJson(basePath, versionJsonReader)
			versionJsonReader.Close()
			if err != nil {
				return err
			}
		}
	}
	if !found {
		return fmt.Errorf("unable to find version.json in %s", forgeJarPath)
	}

	return nil
}

// PrepareLibrariesFromForgeVersionJson processes the version.json content in the given reader
func PrepareLibrariesFromForgeVersionJson(basePath string, reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	var content ForgeVersion
	err := decoder.Decode(&content)
	if err != nil {
		return err
	}

	for _, library := range content.Libraries {
		if library.Serverreq {
			err := DownloadLibrary(basePath, library.Name, library.Url, library.Checksums)
			if err != nil {
				return fmt.Errorf("failed to download %s: %w", library.Name, err)
			}
		}
	}

	err = DownloadMinecraftServer(basePath, content.InheritsFrom)

	return nil
}

func DownloadMinecraftServer(basePath string, version string) error {
	outFilePath := filepath.Join(basePath, fmt.Sprintf("minecraft_server.%s.jar", version))
	exists, err := FileExists(outFilePath)
	if err != nil {
		return fmt.Errorf("failed to check for minecraft server file: %w", err)
	}
	if exists {
		return nil
	}

	context := &minecraftServerUrlContext{
		Version: version,
	}
	var buf strings.Builder
	err = minecraftServerUrl.Execute(&buf, context)
	if err != nil {
		return fmt.Errorf("failed to build URL for minecraft server download: %w", err)
	}

	url := buf.String()
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download minecraft server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return buildDownloadError(url, resp)
	}

	outFile, err := os.OpenFile(outFilePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open minecraft server file for writng: %w", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write minecraft server file: %w", err)
	}

	return nil
}

// expectedChecksums is sha1 with %x string formatted
func DownloadLibrary(outPath string, coordinates string, repoUrl string, expectedChecksums []string) error {
	coordParts := strings.SplitN(coordinates, ":", 3)
	if len(coordParts) < 3 {
		return fmt.Errorf("invalid library coordinate %s", coordinates)
	}
	group, artifact, version := coordParts[0], coordParts[1], coordParts[2]
	groupParts := strings.Split(group, ".")

	// forge manifest's Class-Path expects maven style paths under "libraries/"
	dirOfFile := filepath.Join(outPath, ForgeLibrariesSubpath, filepath.Join(groupParts...), artifact, version)
	pathOfFile := filepath.Join(dirOfFile, fmt.Sprintf("%s-%s.jar", artifact, version))

	// see if the library file already exists
	exists, err := FileExists(pathOfFile)
	if err != nil {
		return fmt.Errorf("failed to check library file existence: %w", err)
	}
	if exists {
		// it exists, so double check the checksum (no pun intended)
		err := VerifyChecksums(pathOfFile, expectedChecksums)
		if err == nil {
			// and checksum is good
			return nil
		}

		// checksum is bad so proceed to re-download it
	}

	var urlPath strings.Builder
	for _, elem := range groupParts {
		urlPath.WriteString(elem)
		urlPath.WriteString("/")
	}

	urlPath.WriteString(artifact)
	urlPath.WriteString("/")
	urlPath.WriteString(version)
	urlPath.WriteString("/")
	urlPath.WriteString(fmt.Sprintf("%s-%s.jar", artifact, version))

	if repoUrl == "" {
		repoUrl = mainLibrariesRepo
	}
	parsedRepoUrl, err := url.Parse(repoUrl)
	if err != nil {
		return fmt.Errorf("failed to parse repo URL: %w", err)
	}

	fullUrl, err := parsedRepoUrl.Parse(urlPath.String())
	if err != nil {
		return fmt.Errorf("failed to build full URL: %w", err)
	}

	fulUrlStr := fullUrl.String()
	resp, err := http.Get(fulUrlStr)
	if err != nil {
		return fmt.Errorf("failed to retrieve library: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return buildDownloadError(fulUrlStr, resp)
	}

	err = os.MkdirAll(dirOfFile, 0755)
	if err != nil {
		return fmt.Errorf("failed to create path for library file: %w", err)
	}

	file, err := os.OpenFile(pathOfFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		file.Close()
		return fmt.Errorf("failed to write output file: %w", err)
	}
	file.Close()

	err = VerifyChecksums(pathOfFile, expectedChecksums)
	if err != nil {
		errRemove := os.Remove(pathOfFile)
		if errRemove != nil {
			return fmt.Errorf("%s while removing file due to %w", errRemove.Error(), err)
		}
		return err
	}

	return nil
}

func VerifyChecksums(filePath string, checksums []string) error {
	// skip checking?
	if checksums == nil || len(checksums) == 0 {
		return nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file for checksum: %w", err)
	}
	defer file.Close()

	h := sha1.New()
	_, err = io.Copy(h, file)
	if err != nil {
		return fmt.Errorf("failed to read file for checksum: %w", err)
	}

	actual := fmt.Sprintf("%x", h.Sum(nil))
	for _, checksum := range checksums {
		if checksum == actual {
			return nil
		}
	}
	return fmt.Errorf("checksum %s of %s was not expected", actual, filePath)
}
