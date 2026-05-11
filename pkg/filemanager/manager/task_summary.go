package manager

import "github.com/cloudreve/Cloudreve/v4/ent/task"

func effectiveTaskSummaryNodeID(status task.Status, nodeID, lastNodeID int) int {
	if nodeID > 0 {
		return nodeID
	}

	switch status {
	case task.StatusCompleted, task.StatusError, task.StatusCanceled:
		return lastNodeID
	default:
		return 0
	}
}
