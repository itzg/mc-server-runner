package cfsync

import (
	"encoding/json"
	"fmt"
	"github.com/karrick/godirwalk"
	"go.uber.org/zap"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const (
	ModsSubdir = "mods"
)

type CfInstalledAddonFile struct {
	DownloadUrl    string
	FileNameOnDisk string
}

type CfInstalledAddon struct {
	InstalledFile CfInstalledAddonFile
}

type CfMinecraftInstance struct {
	BaseModLoader struct {
		DownloadUrl string
	}
	Name            string
	Guid            string
	InstalledAddons []*CfInstalledAddon
}

func LoadMinecraftInstance(r io.Reader) (*CfMinecraftInstance, error) {
	decoder := json.NewDecoder(r)

	var instance CfMinecraftInstance
	err := decoder.Decode(&instance)
	if err != nil {
		return nil, err
	}

	return &instance, nil
}

func PrepareInstanceFromFile(logger *zap.Logger, name string, basePath string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	return PrepareInstance(logger, file, basePath)
}

// PrepareInstance donwloads the appropriate server jar and mods from the given path to a
// minecraftinstance.json file. The server jar's path is returned or an error.
func PrepareInstance(logger *zap.Logger, r io.Reader, basePath string) (string, error) {
	instance, err := LoadMinecraftInstance(r)
	if err != nil {
		return "", fmt.Errorf("failed to load instance file: %w", err)
	}

	logger.Info("Preparing Twitch/Curse instance", zap.String("name", instance.Name))

	forgeJarName, err := PrepareForge(logger, instance.BaseModLoader.DownloadUrl, basePath)
	if err != nil {
		return "", fmt.Errorf("failed to prepare forge: %w", err)
	}

	err = PrepareMods(logger, instance, basePath)
	if err != nil {
		return "", fmt.Errorf("failed to prepare mods: %w", err)
	}

	logger.Debug("Prepared instance", zap.String("name", instance.Name))

	return forgeJarName, nil
}

func PrepareMods(logger *zap.Logger, instance *CfMinecraftInstance, basePath string) error {
	logger.Info("Preparing mods")

	modsPath := filepath.Join(basePath, ModsSubdir)

	err := os.MkdirAll(modsPath, 0755)
	if err != nil {
		return err
	}

	existing, err := LocateExistingModFiles(modsPath)
	if err != nil {
		return fmt.Errorf("failed to locate existing mod files: %w", err)
	}

	latestMods := NewStringSet()
	for _, addon := range instance.InstalledAddons {
		filename := addon.InstalledFile.FileNameOnDisk
		latestMods.Add(filename)

		modPath := filepath.Join(modsPath, filename)
		logger.Debug("Preparing mod", zap.String("name", filename))
		err := PrepareModFile(logger, modPath, addon.InstalledFile.DownloadUrl)
		if err != nil {
			return fmt.Errorf("failed to prepare mod file: %w", err)
		}
	}

	removeThese := existing.Difference(latestMods)
	for name := range removeThese {
		err := os.Remove(filepath.Join(modsPath, name))
		if err != nil {
			return fmt.Errorf("failed to remove old mod file: %w", err)
		}
	}

	return nil
}

func LocateExistingModFiles(modsPath string) (StringSet, error) {
	names, err := godirwalk.ReadDirnames(modsPath, nil)
	if err != nil {
		return nil, err
	}

	return NewStringSet(names...), nil
}

func PrepareModFile(logger *zap.Logger, modPath string, downloadUrl string) error {
	exists, err := FileExists(modPath)
	if err != nil {
		return fmt.Errorf("failed to check if mod file exists: %w", err)
	}
	if exists {
		return nil
	}

	logger.Info("Downloading mod", zap.String("file", filepath.Base(modPath)))
	resp, err := http.Get(downloadUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return buildDownloadError(downloadUrl, resp)
	}

	file, err := os.OpenFile(modPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create mod file: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write mod file: %w", err)
	}

	return nil
}
