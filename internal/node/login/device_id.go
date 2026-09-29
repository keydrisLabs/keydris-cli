package login

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DeviceIDFile is installation identity, not a credential. Logout preserves it;
// an explicit reset removes it. The API still verifies owner/org/agent binding.
const DeviceIDFile = "device-id"

var deviceUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func enrollmentDeviceID(dir string) (string, error) {
	path := filepath.Join(dir, DeviceIDFile)
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if !deviceUUID.MatchString(id) {
			return "", fmt.Errorf("invalid stored device ID; enrollment stopped to avoid duplicating the device")
		}
		return id, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Migrate the server-issued identity before allocating a new installation ID.
	id := ""
	if data, err := os.ReadFile(filepath.Join(dir, WhoamiFile)); err == nil {
		var existing Identity
		if err := json.Unmarshal(data, &existing); err != nil {
			return "", fmt.Errorf("invalid identity metadata; enrollment stopped to avoid duplicating the device")
		}
		id = existing.DeviceID
		if id != "" && !deviceUUID.MatchString(id) {
			return "", fmt.Errorf("invalid device ID in identity metadata")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if id == "" {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return "", err
		}
		value[6] = (value[6] & 0x0f) | 0x40
		value[8] = (value[8] & 0x3f) | 0x80
		id = fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Exclusive creation makes concurrent first enrollments share one identity.
	// A reader racing the write fails safely and can retry; it never invents a second ID.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return enrollmentDeviceID(dir)
	}
	if err != nil {
		return "", err
	}
	_, writeErr := file.WriteString(id + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return id, nil
}
