package manager

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
)

type slaveTaskWarningLogger interface {
	Warning(string, ...interface{})
}

func clearSlaveTaskBestEffort(ctx context.Context, logger slaveTaskWarningLogger, node cluster.Node, taskID int) {
	if taskID == 0 || node == nil {
		return
	}

	if _, err := node.GetTask(ctx, taskID, true); err != nil && logger != nil {
		logger.Warning("Failed to clear slave content processing task %d: %s", taskID, err)
	}
}
