package manager

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
)

func slaveTaskDiagnostic(summary *cluster.SlaveTaskSummary) string {
	if summary == nil {
		return ""
	}

	parts := make([]string, 0, 2)
	if summary.DisplayType != "" {
		parts = append(parts, fmt.Sprintf("display_type=%s", summary.DisplayType))
	}

	if summary.Summary != nil && len(summary.Summary.Props) > 0 {
		if raw, err := json.Marshal(summary.Summary.Props); err == nil {
			parts = append(parts, fmt.Sprintf("summary=%s", string(raw)))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return " [" + strings.Join(parts, ", ") + "]"
}
