package cfsync

import (
	"fmt"
	"net/http"
	"os"
)

func buildDownloadError(url string, resp *http.Response) error {
	return fmt.Errorf("download from %s failed with %d %s", url, resp.StatusCode, resp.Status)
}

func FileExists(name string) (bool, error) {
	_, err := os.Stat(name)
	if err == nil {
		return true, nil
	} else {
		if os.IsNotExist(err) {
			return false, nil
		} else {
			return false, err
		}
	}
}
