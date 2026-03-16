package dbfs

import (
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func ensureCapability(target *File, capabilities ...NavigatorCapability) error {
	if target == nil || target.Capabilities() == nil {
		return nil
	}

	for _, capability := range capabilities {
		if !target.Capabilities().Enabled(int(capability)) {
			return fs.ErrNotSupportedAction.WithError(fmt.Errorf("action %q is not allowed on current file", capability))
		}
	}

	return nil
}
